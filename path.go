package lja

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
)

const (
	programName                           = "lja"
	vmNamePrefix                          = "lja-"
	vmSlugMaxLength                       = 20
	vmHashLength                          = 12
	defaultProjectSlug                    = "project"
	privateStateDirectoryMode os.FileMode = 0o700
	lockDirectoryName                     = "locks"
	lockFileSuffix                        = ".lock"
	projectConfigName                     = ".lja.yaml"
	globalConfigName                      = "config.yaml"
	defaultConfigDirectory                = ".config"
	defaultDataDirectory                  = ".local/share"
	sharedStateDirectoryName              = "agents"
	stateDirectoryEnv                     = "LJA_STATE_DIR"
	xdgConfigHomeEnv                      = "XDG_CONFIG_HOME"
	xdgDataHomeEnv                        = "XDG_DATA_HOME"
	xdgStateHomeEnv                       = "XDG_STATE_HOME"
	xdgCacheHomeEnv                       = "XDG_CACHE_HOME"
	stateDirectoryFlag                    = "--state-dir"
	projectStateFlag                      = "--project-state"
	configDirectoryName                   = "config"
	ProgramName                           = programName
	VMNamePrefix                          = vmNamePrefix
	ProjectConfigName                     = projectConfigName
)

var darwinPublicPathAliases = []struct {
	publicPath  string
	privatePath string
}{
	{publicPath: "/etc", privatePath: "/private/etc"},
	{publicPath: "/tmp", privatePath: "/private/tmp"},
	{publicPath: "/var", privatePath: "/private/var"},
}

type Error struct {
	Message string
	Cause   error
}

func (e *Error) Error() string {
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func ljaError(format string, arguments ...any) error {
	wrapped := fmt.Errorf(format, arguments...)
	return &Error{Message: wrapped.Error(), Cause: wrapped}
}

func homeDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ljaError("cannot determine home directory: %w", err)
	}
	if home == "" {
		return "", ljaError("home directory is empty")
	}
	return home, nil
}

func expandHome(path string) (string, error) {
	if path == "~" {
		return homeDirectory()
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := homeDirectory()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func normalizeDarwinCanonicalPath(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	for _, alias := range darwinPublicPathAliases {
		if path != alias.privatePath && !strings.HasPrefix(path, alias.privatePath+string(filepath.Separator)) {
			continue
		}
		return alias.publicPath + strings.TrimPrefix(path, alias.privatePath)
	}
	return path
}

func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", ljaError("path must not be empty")
	}
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", ljaError("cannot make path absolute %s: %w", path, err)
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return normalizeDarwinCanonicalPath(filepath.Clean(resolved)), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", ljaError("cannot resolve path %s: %w", path, err)
	}

	missing := make([]string, 0)
	probe := absolute
	for {
		_, statErr := os.Lstat(probe)
		if statErr == nil {
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", ljaError("cannot inspect path %s: %w", probe, statErr)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return normalizeDarwinCanonicalPath(absolute), nil
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
	resolvedProbe, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", ljaError("cannot resolve path %s: %w", path, err)
	}
	for _, m := range slices.Backward(missing) {
		resolvedProbe = filepath.Join(resolvedProbe, m)
	}
	return normalizeDarwinCanonicalPath(filepath.Clean(resolvedProbe)), nil
}

func canonicalProjectPath(path string) (string, error) {
	return canonicalPath(path)
}

func CanonicalProjectPath(path string) (string, error) {
	return canonicalProjectPath(path)
}

func ProjectSlug(project string) (string, error) {
	canonical, err := canonicalProjectPath(project)
	if err != nil {
		return "", err
	}
	base := strings.ToLower(filepath.Base(canonical))
	var builder strings.Builder
	separator := false
	for _, character := range base {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			if _, err := builder.WriteRune(character); err != nil {
				return "", ljaError("cannot build project slug: %w", err)
			}
			separator = false
		} else if !separator {
			if err := builder.WriteByte('-'); err != nil {
				return "", ljaError("cannot build project slug: %w", err)
			}
			separator = true
		}
	}
	slug := builder.String()
	runes := []rune(slug)
	if len(runes) > vmSlugMaxLength {
		slug = string(runes[:vmSlugMaxLength])
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return defaultProjectSlug, nil
	}
	return slug, nil
}

func ProjectVMName(project string) (string, error) {
	canonical, err := canonicalProjectPath(project)
	if err != nil {
		return "", err
	}
	slug, err := ProjectSlug(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(canonical))
	hashText := hex.EncodeToString(digest[:])
	return vmNamePrefix + slug + "-" + hashText[:vmHashLength], nil
}

func projectVMName(project string) (string, error) {
	return ProjectVMName(project)
}

func ResolveProject(projectArgument, currentDirectory string) (string, error) {
	candidate := projectArgument
	if candidate == "" {
		candidate = currentDirectory
	}
	if candidate == "" {
		var err error
		candidate, err = os.Getwd()
		if err != nil {
			return "", ljaError("cannot get current directory: %w", err)
		}
	}
	project, err := canonicalProjectPath(candidate)
	if err != nil {
		return "", ljaError("cannot resolve project %s: %w", candidate, err)
	}
	info, err := os.Stat(project)
	if err != nil {
		return "", ljaError("cannot resolve project %s: %w", candidate, err)
	}
	if !info.IsDir() {
		return "", ljaError("project is not a directory: %s", project)
	}
	return project, nil
}

func resolveProject(projectArgument, currentDirectory string) (string, error) {
	return ResolveProject(projectArgument, currentDirectory)
}

func pathIsWithin(path, directory string) (bool, error) {
	relative, err := filepath.Rel(directory, path)
	if err != nil {
		return false, ljaError("cannot compare paths %s and %s: %w", path, directory, err)
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func configuredHostDirectory(environment map[string]string, name, defaultPath string) (string, error) {
	value, present := environment[name]
	if !present {
		value = defaultPath
	} else if value == "" {
		return "", ljaError("host environment %s must be a non-empty path", name)
	}
	return canonicalPath(value)
}

func ensurePrivateDirectory(directory string) error {
	if directory == "" {
		return ljaError("state directory is empty")
	}
	if err := os.MkdirAll(directory, privateStateDirectoryMode); err != nil {
		return ljaError("cannot create state directory %s: %w", directory, err)
	}
	return nil
}

func defaultLockDirectory(project, stateRoot string) (string, error) {
	home, err := homeDirectory()
	if err != nil {
		return "", err
	}
	temporary := os.TempDir()
	candidates := []string{
		filepath.Join(temporary, programName, lockDirectoryName),
		filepath.Join(string(filepath.Separator), "var", "tmp", programName, lockDirectoryName),
		filepath.Join(home, ".cache", programName, lockDirectoryName),
	}
	mounts, err := expectedMountPaths(project, stateRoot)
	if err != nil {
		return "", err
	}
	for _, candidate := range candidates {
		resolved, resolveErr := canonicalPath(candidate)
		if resolveErr != nil {
			return "", resolveErr
		}
		inside := false
		for _, mount := range mounts {
			within, withinErr := pathIsWithin(resolved, mount)
			if withinErr != nil {
				return "", withinErr
			}
			if within {
				inside = true
				break
			}
		}
		if !inside {
			return candidate, nil
		}
	}
	return "", ljaError("cannot choose a host lock directory outside mounts for %s", project)
}

func environmentMap() map[string]string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

type projectDirectoryPolicy struct {
	home string
	uid  int
	stat func(string) (os.FileInfo, error)
}

func newProjectDirectoryPolicy() (projectDirectoryPolicy, error) {
	home, err := homeDirectory()
	if err != nil {
		return projectDirectoryPolicy{}, err
	}
	home, err = canonicalPath(home)
	if err != nil {
		return projectDirectoryPolicy{}, err
	}
	return projectDirectoryPolicy{home: home, uid: os.Getuid(), stat: os.Stat}, nil
}

func (policy projectDirectoryPolicy) rejection(directory string) (string, error) {
	if directory == policy.home {
		return "project must not be the user home directory", nil
	}
	info, err := policy.stat(directory)
	if err != nil {
		return "", ljaError("cannot inspect project directory %s: %w", directory, err)
	}
	if !info.IsDir() {
		return "project is not a directory", nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ljaError("cannot determine project directory owner: %s", directory)
	}
	if uint64(stat.Uid) != uint64(policy.uid) {
		return "project directory is not owned by the current user", nil
	}
	return "", nil
}

func validateProjectDirectory(directory string) error {
	canonical, err := canonicalProjectPath(directory)
	if err != nil {
		return err
	}
	policy, err := newProjectDirectoryPolicy()
	if err != nil {
		return err
	}
	reason, err := policy.rejection(canonical)
	if err != nil {
		return err
	}
	if reason != "" {
		return ljaError("%s: %s", reason, canonical)
	}
	return nil
}
