package lja

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	gitCommand              = "git"
	gitConfigName           = ".gitconfig"
	gitIncludeDirectory     = ".config/lja/git/includes"
	gitIncludeMaxDepth      = 128
	gitIncludePathKey       = "include.path"
	gitIncludeIfPrefix      = "includeif."
	gitPathKeySuffix        = ".path"
	gitExcludesFileKey      = "core.excludesfile"
	gitConfigWriteTemporary = ".lja-git.XXXXXXXXXX"
	gitConfigCountEnv       = "GIT_CONFIG_COUNT"
	gitConfigKeyEnvPrefix   = "GIT_CONFIG_KEY_"
	gitConfigValueEnvPrefix = "GIT_CONFIG_VALUE_"
	githubHTTPSBase         = "https://github.com/"
	githubCredentialKey     = "credential.https://github.com.helper"
	githubGitHelper         = "!gh auth git-credential"
)

var githubURLInputPrefixes = []string{
	"git@github.com:",
	"ssh://git@github.com/",
	"https://github.com/",
}

type gitConfigEnvironmentEntry struct {
	key   string
	value string
}

type gitURLRewrite struct {
	path  string
	key   string
	value string
	base  string
}

func parseGitConfigEnvironment(environment map[string]string) ([]gitConfigEnvironmentEntry, error) {
	countValue, countPresent := environment[gitConfigCountEnv]
	keyNames := make(map[int]string)
	valueNames := make(map[int]string)
	parseNames := func(prefix string, destination map[int]string) error {
		for name := range environment {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			suffix := strings.TrimPrefix(name, prefix)
			index, err := strconv.Atoi(suffix)
			if suffix == "" || err != nil || index < 0 || strconv.Itoa(index) != suffix {
				return ljaError("invalid %s environment variable %s", prefix, name)
			}
			if _, present := destination[index]; present {
				return ljaError("duplicate %s environment index %d", prefix, index)
			}
			destination[index] = name
		}
		return nil
	}
	if err := parseNames(gitConfigKeyEnvPrefix, keyNames); err != nil {
		return nil, err
	}
	if err := parseNames(gitConfigValueEnvPrefix, valueNames); err != nil {
		return nil, err
	}
	if !countPresent {
		if len(keyNames) != 0 || len(valueNames) != 0 {
			return nil, ljaError("%s is required when GIT_CONFIG_KEY_* or GIT_CONFIG_VALUE_* is set", gitConfigCountEnv)
		}
		return nil, nil
	}
	count, err := strconv.Atoi(countValue)
	if err != nil || count < 0 {
		return nil, ljaError("invalid %s value", gitConfigCountEnv)
	}
	if count > len(environment) {
		return nil, ljaError("%s is missing indexed entries", gitConfigCountEnv)
	}
	for index := range keyNames {
		if index >= count {
			return nil, ljaError("GIT_CONFIG_KEY_%d is outside %s", index, gitConfigCountEnv)
		}
	}
	for index := range valueNames {
		if index >= count {
			return nil, ljaError("GIT_CONFIG_VALUE_%d is outside %s", index, gitConfigCountEnv)
		}
	}
	entries := make([]gitConfigEnvironmentEntry, count)
	for index := 0; index < count; index++ {
		keyName, keyPresent := keyNames[index]
		valueName, valuePresent := valueNames[index]
		if !keyPresent || !valuePresent {
			return nil, ljaError("%s is missing GIT_CONFIG entry %d", gitConfigCountEnv, index)
		}
		key := environment[keyName]
		value := environment[valueName]
		if key == "" || strings.IndexByte(key, 0) >= 0 || strings.ContainsAny(key, "=") {
			return nil, ljaError("GIT_CONFIG_KEY_%d is invalid", index)
		}
		for _, character := range key {
			if unicode.IsControl(character) || unicode.IsSpace(character) {
				return nil, ljaError("GIT_CONFIG_KEY_%d is invalid", index)
			}
		}
		if strings.IndexByte(value, 0) >= 0 {
			return nil, ljaError("GIT_CONFIG_VALUE_%d contains a NUL character", index)
		}
		entries[index] = gitConfigEnvironmentEntry{key: key, value: value}
	}
	return entries, nil
}

func githubGitEnvironment(environment map[string]string) (map[string]string, error) {
	entries, err := parseGitConfigEnvironment(environment)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(environment)+5)
	for name, value := range environment {
		result[name] = value
	}
	managed := []gitConfigEnvironmentEntry{
		{key: githubCredentialKey, value: ""},
		{key: githubCredentialKey, value: githubGitHelper},
		{key: "url." + githubHTTPSBase + ".insteadOf", value: githubURLInputPrefixes[0]},
		{key: "url." + githubHTTPSBase + ".insteadOf", value: githubURLInputPrefixes[1]},
	}
	for index, entry := range managed {
		entryIndex := len(entries) + index
		result[gitConfigKeyEnvPrefix+strconv.Itoa(entryIndex)] = entry.key
		result[gitConfigValueEnvPrefix+strconv.Itoa(entryIndex)] = entry.value
	}
	result[gitConfigCountEnv] = strconv.Itoa(len(entries) + len(managed))
	return result, nil
}

func gitURLRewriteBase(key string) (string, bool) {
	lower := strings.ToLower(key)
	for _, suffix := range []string{".insteadof", ".pushinsteadof"} {
		if strings.HasPrefix(lower, "url.") && strings.HasSuffix(lower, suffix) && len(key) > len("url.")+len(suffix) {
			return key[len("url.") : len(key)-len(suffix)], true
		}
	}
	return "", false
}

func copiedGitURLRewrites(copies map[string][]byte) ([]gitURLRewrite, error) {
	if len(copies) == 0 {
		return nil, nil
	}
	temporaryDirectory, err := os.MkdirTemp("", "lja-github-git-")
	if err != nil {
		return nil, ljaError("cannot inspect copied Git configuration: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(temporaryDirectory); removeErr != nil {
			slog.Warn("cannot remove temporary Git configuration directory", "path", temporaryDirectory, "error", removeErr)
		}
	}()
	relatives := make([]string, 0, len(copies))
	for relative := range copies {
		relatives = append(relatives, relative)
	}
	sort.Strings(relatives)
	rewrites := make([]gitURLRewrite, 0)
	for index, relative := range relatives {
		path := filepath.Join(temporaryDirectory, strconv.Itoa(index)+".gitconfig")
		if err := os.WriteFile(path, copies[relative], 0o600); err != nil {
			return nil, ljaError("cannot inspect copied Git configuration %s: %w", relative, err)
		}
		entries, err := hostGitConfig(path, "--null", "--list")
		if err != nil {
			slog.Debug("skipping copied Git file that is not a Git configuration", "path", relative, "error", err)
			continue
		}
		for _, entry := range bytes.Split(entries, []byte{0}) {
			if len(entry) == 0 {
				continue
			}
			separator := bytes.IndexByte(entry, '\n')
			if separator < 0 {
				return nil, ljaError("cannot inspect copied Git configuration %s: malformed entry", relative)
			}
			key := string(entry[:separator])
			base, present := gitURLRewriteBase(key)
			if !present {
				continue
			}
			rewrites = append(rewrites, gitURLRewrite{path: relative, key: key, value: string(entry[separator+1:]), base: base})
		}
	}
	return rewrites, nil
}

func githubGitURLRewriteConflicts(copies map[string][]byte) error {
	rewrites, err := copiedGitURLRewrites(copies)
	if err != nil {
		return err
	}
	for _, rewrite := range rewrites {
		if strings.EqualFold(rewrite.base, githubHTTPSBase) {
			continue
		}
		lowerValue := strings.ToLower(rewrite.value)
		for _, prefix := range githubURLInputPrefixes {
			lowerPrefix := strings.ToLower(prefix)
			if strings.HasPrefix(lowerValue, lowerPrefix) || strings.HasPrefix(lowerPrefix, lowerValue) {
				return ljaError("copied Git config %s has conflicting GitHub URL rewrite %s=%s; HTTPS authentication cannot be enforced", rewrite.path, rewrite.key, rewrite.value)
			}
		}
	}
	return nil
}

type gitReplacement struct {
	key    string
	value  string
	target string
}

func hostGitConfig(path string, arguments ...string) ([]byte, error) {
	commandArguments := []string{"config", "--file", path, "--no-includes"}
	commandArguments = append(commandArguments, arguments...)
	command := exec.Command(gitCommand, commandArguments...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
				return nil, ljaError("cannot process Git config %s: Git exited %d: %s", path, exitError.ExitCode(), diagnostic)
			}
			return nil, ljaError("cannot process Git config %s: Git exited %d", path, exitError.ExitCode())
		}
		return nil, ljaError("cannot run host Git for config %s: %w", path, err)
	}
	return stdout.Bytes(), nil
}

func sourceDestination(hostHome string, source string, destinations map[string]string) (string, error) {
	relative, err := filepath.Rel(hostHome, source)
	insideHome := err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	if !insideHome {
		digest := sha256.Sum256([]byte(source))
		relative = filepath.Join(gitIncludeDirectory, hex.EncodeToString(digest[:]), filepath.Base(source))
	}
	relative = filepath.ToSlash(relative)
	if previous, present := destinations[relative]; present && previous != source {
		return "", ljaError("Git config destination collision: %s", source)
	}
	destinations[relative] = source
	return relative, nil
}

func expandedGitPath(value string, hostHome string) (string, error) {
	if value == "~" || strings.HasPrefix(value, "~/") {
		return hostHome + value[1:], nil
	}
	expanded, err := expandHome(value)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(value, "~") {
		userValue := strings.TrimPrefix(value, "~")
		userName, userPath, hasPath := strings.Cut(userValue, "/")
		userRecord, lookupErr := user.Lookup(userName)
		if lookupErr != nil || userRecord.HomeDir == "" {
			return "", ljaError("unsupported Git config path: %s", value)
		}
		if hasPath {
			return filepath.Join(userRecord.HomeDir, userPath), nil
		}
		return userRecord.HomeDir, nil
	}
	if strings.HasPrefix(expanded, "~") || strings.HasPrefix(expanded, "%(prefix)") {
		return "", ljaError("unsupported Git config path: %s", value)
	}
	return expanded, nil
}

func GitConfigCopies(hostHome string) (map[string][]byte, error) {
	return gitConfigCopiesForGPG(hostHome, false)
}

func gitConfigCopiesForGPG(hostHome string, forwardGPG bool) (map[string][]byte, error) {
	absoluteHome, err := filepath.Abs(hostHome)
	if err != nil {
		return nil, ljaError("cannot copy host Git configuration: %w", err)
	}
	copies := make(map[string][]byte)
	destinations := make(map[string]string)
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	copyExcludesFile := func(source string) (string, error) {
		relative, destinationErr := sourceDestination(absoluteHome, source, destinations)
		if destinationErr != nil {
			return "", destinationErr
		}
		content, readErr := os.ReadFile(source)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				slog.Debug("optional Git excludes file missing", "source", source)
				return relative, nil
			}
			return "", readErr
		}
		if previous, present := copies[relative]; present && !bytes.Equal(previous, content) {
			return "", ljaError("Git config destination collision: %s", source)
		}
		copies[relative] = content
		return relative, nil
	}
	var visit func(string) (string, error)
	visit = func(source string) (string, error) {
		relative, destinationErr := sourceDestination(absoluteHome, source, destinations)
		if destinationErr != nil {
			return "", destinationErr
		}
		if len(visiting) >= gitIncludeMaxDepth {
			return "", ljaError("Git config include depth exceeded: %s", source)
		}
		identity, identityErr := canonicalPath(source)
		if identityErr != nil {
			return "", identityErr
		}
		if visiting[identity] {
			return "", ljaError("recursive Git config include cycle: %s", source)
		}
		if visited[source] {
			return relative, nil
		}
		content, readErr := os.ReadFile(source)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				slog.Debug("optional Git config missing", "source", source)
				return relative, nil
			}
			return "", readErr
		}
		visiting[identity] = true
		temporaryDirectory, tempErr := os.MkdirTemp("", "lja-git-")
		if tempErr != nil {
			delete(visiting, identity)
			return "", tempErr
		}
		defer func() {
			if removeErr := os.RemoveAll(temporaryDirectory); removeErr != nil {
				slog.Warn("cannot remove temporary Git config directory", "path", temporaryDirectory, "error", removeErr)
			}
		}()
		temporaryPath := filepath.Join(temporaryDirectory, gitConfigName)
		if writeErr := os.WriteFile(temporaryPath, content, 0o600); writeErr != nil {
			delete(visiting, identity)
			return "", writeErr
		}
		entries, gitErr := hostGitConfig(temporaryPath, "--null", "--list")
		if gitErr != nil {
			delete(visiting, identity)
			return "", gitErr
		}
		replacements := make(map[gitReplacement]bool)
		for _, entry := range bytes.Split(entries, []byte{0}) {
			if len(entry) == 0 {
				continue
			}
			separator := bytes.IndexByte(entry, '\n')
			keyBytes := entry
			valueBytes := []byte{}
			if separator >= 0 {
				keyBytes = entry[:separator]
				valueBytes = entry[separator+1:]
			}
			key := string(keyBytes)
			if forwardGPG && (key == "gpg.program" || key == "gpg.openpgp.program") {
				if _, err := hostGitConfig(temporaryPath, "--replace-all", key, gpgCommand); err != nil {
					return "", err
				}
			}
			isInclude := key == gitIncludePathKey || (strings.HasPrefix(key, gitIncludeIfPrefix) && strings.HasSuffix(key, gitPathKeySuffix))
			if key == gitExcludesFileKey && (separator < 0 || len(valueBytes) == 0) {
				continue
			}
			if !isInclude && key != gitExcludesFileKey {
				continue
			}
			if separator < 0 || len(valueBytes) == 0 {
				delete(visiting, identity)
				return "", ljaError("empty Git include path in %s", source)
			}
			value := string(valueBytes)
			expanded, expandErr := expandedGitPath(value, absoluteHome)
			if expandErr != nil {
				delete(visiting, identity)
				return "", expandErr
			}
			child := expanded
			if !filepath.IsAbs(child) {
				child = filepath.Join(filepath.Dir(source), child)
			}
			child, err = filepath.Abs(child)
			if err != nil {
				delete(visiting, identity)
				return "", err
			}
			var target string
			if key == gitExcludesFileKey {
				target, err = copyExcludesFile(child)
			} else {
				target, err = visit(child)
			}
			if err != nil {
				delete(visiting, identity)
				return "", err
			}
			replacements[gitReplacement{key: key, value: value, target: "~/" + target}] = true
		}
		replacementList := make([]gitReplacement, 0, len(replacements))
		for replacement := range replacements {
			replacementList = append(replacementList, replacement)
		}
		sort.Slice(replacementList, func(left int, right int) bool {
			if replacementList[left].key != replacementList[right].key {
				return replacementList[left].key < replacementList[right].key
			}
			if replacementList[left].value != replacementList[right].value {
				return replacementList[left].value < replacementList[right].value
			}
			return replacementList[left].target < replacementList[right].target
		})
		for _, replacement := range replacementList {
			if _, err := hostGitConfig(temporaryPath, "--fixed-value", "--replace-all", replacement.key, replacement.target, replacement.value); err != nil {
				delete(visiting, identity)
				return "", err
			}
		}
		updated, readUpdatedErr := os.ReadFile(temporaryPath)
		if readUpdatedErr != nil {
			delete(visiting, identity)
			return "", readUpdatedErr
		}
		copies[relative] = updated
		delete(visiting, identity)
		visited[source] = true
		return relative, nil
	}
	if _, err := visit(filepath.Join(absoluteHome, gitConfigName)); err != nil {
		return nil, ljaError("cannot copy host Git configuration: %w", err)
	}
	return copies, nil
}

func gitConfigCopies(hostHome string) (map[string][]byte, error) {
	return GitConfigCopies(hostHome)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func GitConfigWriteScript(relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", ljaError("Git config destination must be relative to guest home")
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "") {
		return "", ljaError("Git config destination must be relative to guest home")
	}
	for _, part := range parts {
		if part == ".." || part == "" {
			return "", ljaError("Git config destination must be relative to guest home")
		}
	}
	lines := []string{
		"set -eu",
		"umask 077",
		"cd -- \"$HOME\"",
		"temporary_path=",
		"trap 'if [ -n \"$temporary_path\" ]; then rm -f -- \"$temporary_path\"; fi' EXIT",
	}
	for _, part := range parts[:len(parts)-1] {
		component := shellQuote("./" + part)
		lines = append(lines,
			fmt.Sprintf("if [ -L %s ]; then echo 'Git config parent is a symlink' >&2; exit 1; fi", component),
			fmt.Sprintf("if [ ! -d %s ]; then mkdir -- %s; fi", component, component),
			fmt.Sprintf("cd -- %s", component),
		)
	}
	filename := shellQuote("./" + parts[len(parts)-1])
	lines = append(lines,
		fmt.Sprintf("if [ -L %s ] || [ -d %s ]; then echo 'Invalid Git config destination' >&2; exit 1; fi", filename, filename),
		"temporary_path=$(mktemp ./"+gitConfigWriteTemporary+")",
		"cat > \"$temporary_path\"",
		fmt.Sprintf("mv -f -- \"$temporary_path\" %s", filename),
		"temporary_path=",
	)
	return strings.Join(lines, "\n") + "\n", nil
}

func gitConfigWriteScript(relative string) (string, error) {
	return GitConfigWriteScript(relative)
}

func prepareGitInternal(project string, vmName string, limactlCommand string, forwardGPG bool, validateGitHub bool) error {
	home, err := homeDirectory()
	if err != nil {
		return ljaError("cannot prepare Git in VM %s: %w", vmName, err)
	}
	copies, err := gitConfigCopiesForGPG(home, forwardGPG)
	if err != nil {
		return ljaError("cannot prepare Git in VM %s: %w", vmName, err)
	}
	if validateGitHub {
		if err := githubGitURLRewriteConflicts(copies); err != nil {
			return ljaError("cannot prepare GitHub integration in VM %s: %w", vmName, err)
		}
	}
	relatives := make([]string, 0, len(copies))
	for relative := range copies {
		relatives = append(relatives, relative)
	}
	sort.Slice(relatives, func(left int, right int) bool {
		leftRoot := relatives[left] == gitConfigName
		rightRoot := relatives[right] == gitConfigName
		if leftRoot != rightRoot {
			return !leftRoot
		}
		return relatives[left] < relatives[right]
	})
	for _, relative := range relatives {
		script, err := GitConfigWriteScript(relative)
		if err != nil {
			return ljaError("cannot prepare Git in VM %s: %w", vmName, err)
		}
		options := defaultProcessOptions(limactlCommand)
		options.hasInput = true
		options.inputData = copies[relative]
		if _, err := runGuest(project, vmName, []string{shellCommand, shellCommandFlag, script}, options, nil); err != nil {
			return ljaError("cannot prepare Git in VM %s: %w", vmName, err)
		}
	}
	return nil
}

func prepareGit(project string, vmName string, limactlCommand string, forwarding ...bool) error {
	return prepareGitInternal(project, vmName, limactlCommand, len(forwarding) > 0 && forwarding[0], false)
}

func prepareGitWithGitHub(project string, vmName string, limactlCommand string, forwarding ...bool) error {
	return prepareGitInternal(project, vmName, limactlCommand, len(forwarding) > 0 && forwarding[0], true)
}
