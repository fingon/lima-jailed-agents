package lja

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	tomledit "github.com/pelletier/go-toml/v2/unstable/edit"
	"gotest.tools/v3/assert"
)

func TestCodexTrustPersistenceSerializesConcurrentUpdates(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	lockDirectory := filepath.Join(root, "locks")
	projects := make([]string, 8)
	for index := range projects {
		projects[index] = filepath.Join(root, "project-"+strconv.Itoa(index))
		assert.NilError(t, os.Mkdir(projects[index], 0o755))
	}

	start := make(chan struct{})
	errors := make(chan error, len(projects))
	var group sync.WaitGroup
	group.Add(len(projects))
	for _, project := range projects {
		go func() {
			defer group.Done()
			<-start
			errors <- ensureCodexDirectoryTrust(state, []string{project}, lockDirectory)
		}()
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		assert.NilError(t, err)
	}

	content, err := os.ReadFile(filepath.Join(state, codexStateDirectoryName, codexConfigName))
	assert.NilError(t, err)
	document, err := tomledit.Parse(content)
	assert.NilError(t, err)
	for _, project := range projects {
		canonical, err := canonicalProjectPath(project)
		assert.NilError(t, err)
		value, present := document.Get(codexTrustPath(canonical))
		assert.Assert(t, present)
		assert.Equal(t, value, codexTrustedValue)
	}
}

func TestCodexTrustPersistenceAtomicallyReplacesAndPreservesMode(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	state := filepath.Join(root, "state")
	lockDirectory := filepath.Join(root, "locks")
	assert.NilError(t, os.Mkdir(project, 0o755))
	configDirectory := filepath.Join(state, codexStateDirectoryName)
	assert.NilError(t, os.MkdirAll(configDirectory, 0o700))
	configPath := filepath.Join(configDirectory, codexConfigName)
	content := "[projects." + strconv.Quote(project) + "]\ntrust_level = \"untrusted\"\n"
	assert.NilError(t, os.WriteFile(configPath, []byte(content), 0o640))
	beforeInfo, err := os.Stat(configPath)
	assert.NilError(t, err)

	assert.NilError(t, ensureCodexDirectoryTrust(state, []string{project}, lockDirectory))

	afterInfo, err := os.Stat(configPath)
	assert.NilError(t, err)
	assert.Assert(t, !os.SameFile(beforeInfo, afterInfo))
	assert.Equal(t, afterInfo.Mode().Perm(), os.FileMode(0o640))
	updated, err := os.ReadFile(configPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(updated), `trust_level = "trusted"`))
	entries, err := os.ReadDir(configDirectory)
	assert.NilError(t, err)
	for _, entry := range entries {
		assert.Assert(t, !strings.HasPrefix(entry.Name(), ".config-"))
	}
}

func TestCodexTrustPersistenceRejectsSymlinksAndUnsafeLockPaths(t *testing.T) {
	t.Run("configuration symlink", func(t *testing.T) {
		root := t.TempDir()
		project := filepath.Join(root, "project")
		state := filepath.Join(root, "state")
		outside := filepath.Join(root, "outside.toml")
		lockDirectory := filepath.Join(root, "locks")
		assert.NilError(t, os.Mkdir(project, 0o755))
		configDirectory := filepath.Join(state, codexStateDirectoryName)
		assert.NilError(t, os.MkdirAll(configDirectory, 0o700))
		assert.NilError(t, os.WriteFile(outside, []byte("keep\n"), 0o600))
		configPath := filepath.Join(configDirectory, codexConfigName)
		assert.NilError(t, os.Symlink(outside, configPath))

		err := ensureCodexDirectoryTrust(state, []string{project}, lockDirectory)
		assert.ErrorContains(t, err, "must not be a symlink")
		content, err := os.ReadFile(outside)
		assert.NilError(t, err)
		assert.Equal(t, string(content), "keep\n")
		info, err := os.Lstat(configPath)
		assert.NilError(t, err)
		assert.Assert(t, info.Mode()&os.ModeSymlink != 0)
	})

	t.Run("lock path inside state", func(t *testing.T) {
		root := t.TempDir()
		project := filepath.Join(root, "project")
		state := filepath.Join(root, "state")
		assert.NilError(t, os.Mkdir(project, 0o755))
		assert.NilError(t, os.Mkdir(state, 0o700))

		err := ensureCodexDirectoryTrust(state, []string{project}, filepath.Join(state, "locks"))
		assert.ErrorContains(t, err, "inside project or state mount")
		_, statErr := os.Stat(filepath.Join(state, codexStateDirectoryName))
		assert.Assert(t, os.IsNotExist(statErr))
	})
}

func TestCodexTrustPersistenceLeavesFilesUnchangedOnErrors(t *testing.T) {
	tests := []struct {
		name    string
		content func(string) string
		want    string
	}{
		{name: "malformed TOML", content: func(string) string { return "projects =\n" }, want: "invalid Codex TOML"},
		{name: "duplicate definition", content: func(string) string { return "value = 1\nvalue = 2\n" }, want: "invalid Codex TOML"},
		{name: "incompatible trust type", content: func(project string) string {
			return "[projects." + strconv.Quote(project) + "]\ntrust_level = 1\n"
		}, want: "must be a string"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "project")
			state := filepath.Join(root, "state")
			lockDirectory := filepath.Join(root, "locks")
			assert.NilError(t, os.Mkdir(project, 0o755))
			configDirectory := filepath.Join(state, codexStateDirectoryName)
			assert.NilError(t, os.MkdirAll(configDirectory, 0o700))
			configPath := filepath.Join(configDirectory, codexConfigName)
			content := test.content(project)
			assert.NilError(t, os.WriteFile(configPath, []byte(content), 0o640))
			beforeInfo, err := os.Stat(configPath)
			assert.NilError(t, err)

			err = ensureCodexDirectoryTrust(state, []string{project}, lockDirectory)
			assert.ErrorContains(t, err, test.want)

			afterInfo, err := os.Stat(configPath)
			assert.NilError(t, err)
			assert.Assert(t, os.SameFile(beforeInfo, afterInfo))
			assert.Equal(t, afterInfo.Mode().Perm(), os.FileMode(0o640))
			updated, err := os.ReadFile(configPath)
			assert.NilError(t, err)
			assert.Equal(t, string(updated), content)
		})
	}
}
