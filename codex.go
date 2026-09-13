package lja

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	codexStateDirectoryName = ".codex"
	codexConfigName         = "config.toml"
	codexProjectsKey        = "projects"
	codexTrustKey           = "trust_level"
	codexTrustedValue       = "trusted"
)

type tomlTrustSetting struct {
	index   int
	value   string
	comment string
}

type tomlSection struct {
	start int
	end   int
	trust *tomlTrustSetting
}

func tomlSectionKey(keys []string) string {
	return strings.Join(keys, "\x00")
}

func tomlKeys(value string) ([]string, error) {
	keys := make([]string, 0)
	remaining := value
	for {
		remaining = strings.TrimLeftFunc(remaining, unicode.IsSpace)
		if remaining == "" {
			return nil, ljaError("empty TOML key")
		}
		var key string
		if remaining[0] == '"' {
			end := -1
			escaped := false
			for index := 1; index < len(remaining); index++ {
				character := remaining[index]
				if escaped {
					escaped = false
					continue
				}
				if character == '\\' {
					escaped = true
					continue
				}
				if character == '"' {
					end = index
					break
				}
			}
			if end < 0 {
				return nil, ljaError("unsupported TOML quoted key escape")
			}
			quoted := remaining[:end+1]
			if err := json.Unmarshal([]byte(quoted), &key); err != nil {
				return nil, ljaError("unsupported TOML quoted key escape")
			}
			remaining = remaining[end+1:]
		} else if remaining[0] == '\'' {
			end := strings.IndexByte(remaining[1:], '\'')
			if end < 0 {
				return nil, ljaError("unsupported TOML key syntax")
			}
			end++
			key = remaining[1:end]
			remaining = remaining[end+1:]
		} else {
			end := 0
			for end < len(remaining) {
				character := remaining[end]
				if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
					end++
					continue
				}
				break
			}
			if end == 0 {
				return nil, ljaError("unsupported TOML key syntax")
			}
			key = remaining[:end]
			remaining = remaining[end:]
		}
		keys = append(keys, key)
		remaining = strings.TrimLeftFunc(remaining, unicode.IsSpace)
		if remaining == "" {
			return keys, nil
		}
		if remaining[0] != '.' || len(remaining) == 1 {
			return nil, ljaError("unsupported TOML key syntax")
		}
		remaining = remaining[1:]
	}
}

func tomlLineContent(line string) (string, string, error) {
	quote := byte(0)
	escaped := false
	brackets := make([]byte, 0)
	for index := 0; index < len(line); index++ {
		character := line[index]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if character == '\\' && quote == '"' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if character == '#' {
			if len(brackets) != 0 {
				return "", "", ljaError("multiline TOML values are unsupported")
			}
			code := strings.TrimRightFunc(line[:index], unicode.IsSpace)
			return code, line[index:], nil
		}
		if character == '"' || character == '\'' {
			if index+2 < len(line) && line[index+1] == character && line[index+2] == character {
				return "", "", ljaError("multiline TOML strings are unsupported")
			}
			quote = character
			continue
		}
		switch character {
		case '[', '{':
			brackets = append(brackets, character)
		case ']', '}':
			if len(brackets) == 0 || (character == ']' && brackets[len(brackets)-1] != '[') || (character == '}' && brackets[len(brackets)-1] != '{') {
				return "", "", ljaError("unbalanced TOML delimiters")
			}
			brackets = brackets[:len(brackets)-1]
		}
	}
	if quote != 0 || len(brackets) != 0 {
		return "", "", ljaError("multiline or unterminated TOML values are unsupported")
	}
	return strings.TrimRightFunc(line, unicode.IsSpace), "", nil
}

func tomlAssignment(code string) (string, string, bool) {
	quote := byte(0)
	escaped := false
	for index := 0; index < len(code); index++ {
		character := code[index]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if character == '\\' && quote == '"' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if character == '"' || character == '\'' {
			quote = character
			continue
		}
		if character == '=' {
			return strings.TrimSpace(code[:index]), code[index+1:], true
		}
	}
	return "", "", false
}

func splitTomlLines(content string) []string {
	parts := strings.Split(content, "\n")
	lines := make([]string, 0, len(parts))
	for index := 0; index < len(parts)-1; index++ {
		lines = append(lines, parts[index]+"\n")
	}
	if parts[len(parts)-1] != "" {
		lines = append(lines, parts[len(parts)-1])
	}
	return lines
}

func tomlQuotedKey(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\':
			builder.WriteString("\\\\")
		case '"':
			builder.WriteString("\\\"")
		case '\b':
			builder.WriteString("\\b")
		case '\t':
			builder.WriteString("\\t")
		case '\n':
			builder.WriteString("\\n")
		case '\f':
			builder.WriteString("\\f")
		case '\r':
			builder.WriteString("\\r")
		default:
			if character < 0x20 || character == 0x7f {
				builder.WriteString(fmt.Sprintf("\\u%04x", character))
			} else {
				builder.WriteRune(character)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func codexTrustedConfig(content string, directories []string) (string, error) {
	lines := splitTomlLines(content)
	sections := make(map[string]*tomlSection)
	section := []string{}
	currentSectionKey := ""
	for index, line := range lines {
		code, comment, err := tomlLineContent(line)
		if err != nil {
			return "", err
		}
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if strings.HasPrefix(code, "[") {
			if currentSectionKey != "" {
				sections[currentSectionKey].end = index
				currentSectionKey = ""
			}
			arrayTable := strings.HasPrefix(code, "[[")
			delimiterSize := 1
			if arrayTable {
				delimiterSize = 2
			}
			if len(code) < delimiterSize*2 || !strings.HasSuffix(code, strings.Repeat("]", delimiterSize)) {
				return "", ljaError("unsupported TOML table syntax")
			}
			keys, keyErr := tomlKeys(code[delimiterSize : len(code)-delimiterSize])
			if keyErr != nil {
				return "", keyErr
			}
			section = keys
			if len(section) > 0 && section[0] == codexProjectsKey {
				if arrayTable || len(section) > 2 {
					return "", ljaError("unsupported Codex projects table layout")
				}
				key := tomlSectionKey(section)
				if _, present := sections[key]; present {
					return "", ljaError("duplicate Codex projects table")
				}
				sections[key] = &tomlSection{start: index + 1, end: len(lines)}
				currentSectionKey = key
			}
			continue
		}
		keyText, value, found := tomlAssignment(code)
		if !found {
			return "", ljaError("unsupported TOML assignment syntax")
		}
		keys, keyErr := tomlKeys(keyText)
		if keyErr != nil {
			return "", keyErr
		}
		if len(keys) == 0 {
			return "", ljaError("empty TOML key")
		}
		if (len(section) == 0 && keys[0] == codexProjectsKey) || (len(section) == 1 && section[0] == codexProjectsKey) {
			return "", ljaError("inline or dotted Codex project definitions are unsupported")
		}
		if len(section) > 0 && section[0] == codexProjectsKey {
			if len(keys) != 1 {
				return "", ljaError("dotted Codex project settings are unsupported")
			}
			if keys[0] == codexTrustKey {
				sectionValue := sections[tomlSectionKey(section)]
				if sectionValue.trust != nil {
					return "", ljaError("duplicate Codex trust setting")
				}
				trimmedValue := strings.TrimSpace(value)
				if trimmedValue != `"trusted"` && trimmedValue != `'trusted'` && trimmedValue != `"untrusted"` && trimmedValue != `'untrusted'` {
					return "", ljaError("unsupported Codex trust value")
				}
				sectionValue.trust = &tomlTrustSetting{index: index, value: trimmedValue[1 : len(trimmedValue)-1], comment: comment}
			}
		}
	}
	edits := make([]struct {
		index   int
		replace bool
		text    string
	}, 0)
	additions := make([]string, 0)
	setting := codexTrustKey + ` = "` + codexTrustedValue + `"` + "\n"
	seenDirectories := make(map[string]bool)
	for _, directory := range directories {
		canonicalDirectory, err := canonicalProjectPath(directory)
		if err != nil {
			return "", err
		}
		if seenDirectories[canonicalDirectory] {
			continue
		}
		seenDirectories[canonicalDirectory] = true
		key := tomlSectionKey([]string{codexProjectsKey, canonicalDirectory})
		existing, present := sections[key]
		if !present {
			additions = append(additions, "["+codexProjectsKey+"."+tomlQuotedKey(canonicalDirectory)+"]\n"+setting)
			continue
		}
		if existing.trust == nil {
			text := setting
			if existing.end > 0 && !strings.HasSuffix(lines[existing.end-1], "\n") {
				text = "\n" + text
			}
			edits = append(edits, struct {
				index   int
				replace bool
				text    string
			}{index: existing.end, text: text})
			continue
		}
		if existing.trust.value != codexTrustedValue {
			line := lines[existing.trust.index]
			indentation := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			text := indentation + strings.TrimSuffix(setting, "\n")
			if existing.trust.comment != "" {
				text += " " + strings.TrimRight(existing.trust.comment, "\r\n")
			}
			edits = append(edits, struct {
				index   int
				replace bool
				text    string
			}{index: existing.trust.index, replace: true, text: text + "\n"})
		}
	}
	sort.Slice(edits, func(left int, right int) bool {
		return edits[left].index > edits[right].index
	})
	for _, edit := range edits {
		deleteCount := 0
		if edit.replace {
			deleteCount = 1
		}
		updatedLines := make([]string, 0, len(lines)-deleteCount+1)
		updatedLines = append(updatedLines, lines[:edit.index]...)
		updatedLines = append(updatedLines, edit.text)
		updatedLines = append(updatedLines, lines[edit.index+deleteCount:]...)
		lines = updatedLines
	}
	updated := strings.Join(lines, "")
	if len(additions) != 0 {
		if updated != "" && !strings.HasSuffix(updated, "\n") {
			updated += "\n\n"
		} else if updated != "" {
			updated += "\n"
		}
		updated += strings.Join(additions, "\n")
	}
	return updated, nil
}

func CodexTrustedConfig(content string, directories []string) (string, error) {
	return codexTrustedConfig(content, directories)
}

func ensureCodexDirectoryTrust(stateRoot string, directories []string, lockDirectory string) error {
	if len(directories) == 0 {
		return nil
	}
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(canonicalState))
	lockName := "codex-config-" + hex.EncodeToString(digest[:])
	lockErr := withAdvisoryLock(canonicalState, lockName, "", lockDirectory, func(string) error {
		if _, err := EnsureAgentStateDirectories(canonicalState, codexAgentName); err != nil {
			return err
		}
		configPath := filepath.Join(canonicalState, codexStateDirectoryName, codexConfigName)
		if info, statErr := os.Lstat(configPath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return ljaError("Codex configuration must not be a symlink")
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		original := ""
		mode := os.FileMode(0o600)
		if info, statErr := os.Stat(configPath); statErr == nil {
			mode = info.Mode().Perm()
			content, readErr := os.ReadFile(configPath)
			if readErr != nil {
				return readErr
			}
			if !utf8.Valid(content) {
				return ljaError("configuration is not valid UTF-8")
			}
			original = string(content)
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		updated, configErr := codexTrustedConfig(original, directories)
		if configErr != nil {
			return configErr
		}
		if updated == original {
			return nil
		}
		temporary, createErr := os.CreateTemp(filepath.Dir(configPath), ".config-*")
		if createErr != nil {
			return createErr
		}
		temporaryPath := temporary.Name()
		keepTemporary := true
		defer func() {
			if keepTemporary {
				if removeErr := os.Remove(temporaryPath); removeErr != nil && !os.IsNotExist(removeErr) {
					slog.Warn("cannot remove temporary Codex config path", "path", temporaryPath, "error", removeErr)
				}
			}
		}()
		if chmodErr := temporary.Chmod(mode); chmodErr != nil {
			closeErr := temporary.Close()
			if closeErr != nil {
				return fmt.Errorf("%w; closing temporary config: %v", chmodErr, closeErr)
			}
			return chmodErr
		}
		writeErr := error(nil)
		_, writeCountErr := temporary.WriteString(updated)
		if writeCountErr != nil {
			writeErr = writeCountErr
		} else {
			writeErr = temporary.Sync()
		}
		closeErr := temporary.Close()
		if writeErr != nil || closeErr != nil {
			if writeErr != nil && closeErr != nil {
				return fmt.Errorf("%w; closing temporary config: %v", writeErr, closeErr)
			}
			if writeErr != nil {
				return writeErr
			}
			return closeErr
		}
		if renameErr := os.Rename(temporaryPath, configPath); renameErr != nil {
			return renameErr
		}
		keepTemporary = false
		return nil
	})
	if lockErr != nil {
		return ljaError("cannot configure Codex directory trust in %s: %w", filepath.Join(canonicalState, codexStateDirectoryName, codexConfigName), lockErr)
	}
	return nil
}

func EnsureCodexDirectoryTrust(stateRoot string, directories []string, lockDirectory string) error {
	return ensureCodexDirectoryTrust(stateRoot, directories, lockDirectory)
}
