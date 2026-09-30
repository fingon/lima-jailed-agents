package lja

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	claudeGlobalConfigName      = ".claude.json"
	claudeProjectsKey           = "projects"
	claudeTrustKey              = "hasTrustDialogAccepted"
	claudeConfigLockPrefix      = "claude-config-"
	claudeConfigTemporaryPrefix = ".claude.json-"
	claudeConfigDefaultFileMode = os.FileMode(0o600)
	claudeTrustedJSONValue      = "true"
	booleanTypeErrorMessage     = "must be a boolean"
	invalidClaudeConfigMessage  = "invalid Claude configuration"
)

func canonicalClaudeDirectories(directories []string) ([]string, error) {
	canonicalDirectories := make([]string, 0, len(directories))
	seenDirectories := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		canonicalDirectory, err := canonicalProjectPath(directory)
		if err != nil {
			return nil, err
		}
		if _, seen := seenDirectories[canonicalDirectory]; seen {
			continue
		}
		seenDirectories[canonicalDirectory] = struct{}{}
		canonicalDirectories = append(canonicalDirectories, canonicalDirectory)
	}
	return canonicalDirectories, nil
}

func claudeObject(value json.RawMessage, name string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, ljaError("Claude %s must be an object", name)
	}
	return object, nil
}

func claudeTrustValue(value json.RawMessage, directory string) (bool, error) {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return false, ljaError("Claude trust setting for %s %s: %w", directory, booleanTypeErrorMessage, err)
	}
	trust, ok := decoded.(bool)
	if !ok {
		return false, ljaError("Claude trust setting for %s %s", directory, booleanTypeErrorMessage)
	}
	return trust, nil
}

func claudeTrustedConfig(content string, directories []string) (string, error) {
	if !utf8.ValidString(content) {
		return "", ljaError("configuration is not valid UTF-8")
	}
	canonicalDirectories, err := canonicalClaudeDirectories(directories)
	if err != nil {
		return "", err
	}
	root := make(map[string]json.RawMessage)
	if strings.TrimSpace(content) != "" {
		if err := json.Unmarshal([]byte(content), &root); err != nil || root == nil {
			if err != nil {
				return "", ljaError(invalidClaudeConfigMessage+": %w", err)
			}
			return "", ljaError(invalidClaudeConfigMessage + ": top-level value must be an object")
		}
	}
	projects := make(map[string]json.RawMessage)
	if value, present := root[claudeProjectsKey]; present {
		projects, err = claudeObject(value, claudeProjectsKey)
		if err != nil {
			return "", err
		}
	}
	changed := false
	for _, directory := range canonicalDirectories {
		project := make(map[string]json.RawMessage)
		if value, present := projects[directory]; present {
			project, err = claudeObject(value, "project "+directory)
			if err != nil {
				return "", err
			}
		}
		if value, present := project[claudeTrustKey]; present {
			trusted, trustErr := claudeTrustValue(value, directory)
			if trustErr != nil {
				return "", trustErr
			}
			if trusted {
				continue
			}
		}
		project[claudeTrustKey] = json.RawMessage(claudeTrustedJSONValue)
		encodedProject, marshalErr := json.Marshal(project)
		if marshalErr != nil {
			return "", ljaError("cannot encode Claude project trust for %s: %w", directory, marshalErr)
		}
		projects[directory] = json.RawMessage(encodedProject)
		changed = true
	}
	if !changed {
		return content, nil
	}
	encodedProjects, err := json.Marshal(projects)
	if err != nil {
		return "", ljaError("cannot encode Claude projects: %w", err)
	}
	root[claudeProjectsKey] = json.RawMessage(encodedProjects)
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", ljaError("cannot encode Claude configuration: %w", err)
	}
	updated = append(updated, '\n')
	if err := validateClaudeTrustedConfig(string(updated), canonicalDirectories); err != nil {
		return "", err
	}
	return string(updated), nil
}

func validateClaudeTrustedConfig(content string, directories []string) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &root); err != nil || root == nil {
		if err != nil {
			return ljaError(invalidClaudeConfigMessage+" after edit: %w", err)
		}
		return ljaError(invalidClaudeConfigMessage + " after edit: top-level value must be an object")
	}
	projectsValue, present := root[claudeProjectsKey]
	if !present {
		return ljaError("Claude projects missing after edit")
	}
	projects, err := claudeObject(projectsValue, claudeProjectsKey)
	if err != nil {
		return err
	}
	for _, directory := range directories {
		projectValue, present := projects[directory]
		if !present {
			return ljaError("Claude project missing after edit for %s", directory)
		}
		project, projectErr := claudeObject(projectValue, "project "+directory)
		if projectErr != nil {
			return projectErr
		}
		trustValue, present := project[claudeTrustKey]
		if !present {
			return ljaError("Claude trust setting missing after edit for %s", directory)
		}
		trusted, trustErr := claudeTrustValue(trustValue, directory)
		if trustErr != nil {
			return trustErr
		}
		if !trusted {
			return ljaError("Claude trust setting for %s is not accepted after edit", directory)
		}
	}
	return nil
}

func ClaudeTrustedConfig(content string, directories []string) (string, error) {
	return claudeTrustedConfig(content, directories)
}

func ensureClaudeDirectoryTrust(stateRoot string, directories []string, lockDirectory string) error {
	if len(directories) == 0 {
		return nil
	}
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(canonicalState))
	lockName := claudeConfigLockPrefix + hex.EncodeToString(digest[:])
	lockErr := withAdvisoryLock(AdvisoryLockOptions{
		Project:       canonicalState,
		VMName:        lockName,
		LockDirectory: lockDirectory,
	}, func(string) error {
		if _, err := EnsureAgentStateDirectories(canonicalState, claudeAgentName); err != nil {
			return err
		}
		configPath := filepath.Join(canonicalState, claudeStateDirectoryName, claudeGlobalConfigName)
		if info, statErr := os.Lstat(configPath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return ljaError("Claude configuration must not be a symlink")
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		original := ""
		mode := claudeConfigDefaultFileMode
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
		updated, configErr := claudeTrustedConfig(original, directories)
		if configErr != nil {
			return configErr
		}
		if updated == original {
			return nil
		}
		temporary, createErr := os.CreateTemp(filepath.Dir(configPath), claudeConfigTemporaryPrefix)
		if createErr != nil {
			return createErr
		}
		temporaryPath := temporary.Name()
		keepTemporary := true
		defer func() {
			if keepTemporary {
				if removeErr := os.Remove(temporaryPath); removeErr != nil && !os.IsNotExist(removeErr) {
					slog.Warn("cannot remove temporary Claude config path", "path", temporaryPath, "error", removeErr)
				}
			}
		}()
		if chmodErr := temporary.Chmod(mode); chmodErr != nil {
			closeErr := temporary.Close()
			if closeErr != nil {
				return ljaError("%v; closing temporary Claude config: %w", chmodErr, closeErr)
			}
			return chmodErr
		}
		_, writeErr := temporary.WriteString(updated)
		if writeErr == nil {
			writeErr = temporary.Sync()
		}
		closeErr := temporary.Close()
		if writeErr != nil || closeErr != nil {
			if writeErr != nil && closeErr != nil {
				return ljaError("%v; closing temporary Claude config: %w", writeErr, closeErr)
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
		return ljaError("cannot configure Claude directory trust in %s: %w", filepath.Join(canonicalState, claudeStateDirectoryName, claudeGlobalConfigName), lockErr)
	}
	return nil
}

func EnsureClaudeDirectoryTrust(stateRoot string, directories []string, lockDirectory string) error {
	return ensureClaudeDirectoryTrust(stateRoot, directories, lockDirectory)
}
