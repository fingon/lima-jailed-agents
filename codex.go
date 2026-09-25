package lja

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2/unstable"
	tomledit "github.com/pelletier/go-toml/v2/unstable/edit"
)

const (
	codexStateDirectoryName = ".codex"
	codexConfigName         = "config.toml"
	codexProjectsKey        = "projects"
	codexTrustKey           = "trust_level"
	codexTrustedValue       = "trusted"
	codexUntrustedValue     = "untrusted"
	codexTrustedTOMLValue   = `"trusted"`
	invalidCodexTOMLMessage = "invalid Codex TOML"
)

func canonicalCodexDirectories(directories []string) ([]string, error) {
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

func codexProjectPath(directory string) []string {
	return []string{codexProjectsKey, directory}
}

func codexTrustPath(directory string) []string {
	return []string{codexProjectsKey, directory, codexTrustKey}
}

func codexIsTable(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func codexTrustValue(value any, directory string) (string, error) {
	trust, ok := value.(string)
	if !ok {
		return "", ljaError("Codex trust setting for %s must be a string", directory)
	}
	switch trust {
	case codexTrustedValue, codexUntrustedValue:
		return trust, nil
	default:
		return "", ljaError("unsupported Codex trust value for %s: %s", directory, trust)
	}
}

func codexSetDirectoryTrust(document *tomledit.Document, directory string) (bool, error) {
	projects, present := document.Get([]string{codexProjectsKey})
	if present && !codexIsTable(projects) {
		return false, ljaError("Codex projects must be a table")
	}
	projectPath := codexProjectPath(directory)
	project, present := document.Get(projectPath)
	if present && !codexIsTable(project) {
		return false, ljaError("Codex project %s must be a table", directory)
	}
	trustPath := codexTrustPath(directory)
	value, present := document.Get(trustPath)
	if present {
		trust, err := codexTrustValue(value, directory)
		if err != nil {
			return false, err
		}
		if trust == codexTrustedValue {
			return false, nil
		}
	}
	if err := document.Set(trustPath, unstable.RawMessage(codexTrustedTOMLValue)); err != nil {
		return false, ljaError("cannot edit Codex trust for %s: %w", directory, err)
	}
	return true, nil
}

func validateCodexTrust(document *tomledit.Document, directories []string) error {
	for _, directory := range directories {
		value, present := document.Get(codexTrustPath(directory))
		if !present {
			return ljaError("Codex trust setting missing after edit for %s", directory)
		}
		trust, err := codexTrustValue(value, directory)
		if err != nil {
			return err
		}
		if trust != codexTrustedValue {
			return ljaError("Codex trust setting for %s is not trusted after edit", directory)
		}
	}
	return nil
}

func codexTrustedConfig(content string, directories []string) (string, error) {
	if !utf8.ValidString(content) {
		return "", ljaError("configuration is not valid UTF-8")
	}
	canonicalDirectories, err := canonicalCodexDirectories(directories)
	if err != nil {
		return "", err
	}
	document, err := tomledit.Parse([]byte(content))
	if err != nil {
		return "", ljaError(invalidCodexTOMLMessage+": %w", err)
	}
	changed := false
	for _, directory := range canonicalDirectories {
		directoryChanged, setErr := codexSetDirectoryTrust(document, directory)
		if setErr != nil {
			return "", setErr
		}
		changed = changed || directoryChanged
	}
	updated := string(document.Bytes())
	validatedDocument, err := tomledit.Parse([]byte(updated))
	if err != nil {
		return "", ljaError("edited Codex TOML is invalid: %w", err)
	}
	if err := validateCodexTrust(validatedDocument, canonicalDirectories); err != nil {
		return "", err
	}
	if !changed {
		return content, nil
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
	lockErr := withAdvisoryLock(AdvisoryLockOptions{
		Project:       canonicalState,
		VMName:        lockName,
		LockDirectory: lockDirectory,
	}, func(string) error {
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
				return fmt.Errorf("%w; closing temporary config: %w", chmodErr, closeErr)
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
				return fmt.Errorf("%w; closing temporary config: %w", writeErr, closeErr)
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
