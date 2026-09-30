package lja

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
)

func TestClaudeTrustedConfigPreservesStateAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	workingDirectory := filepath.Join(root, "working")
	assert.NilError(t, os.Mkdir(project, 0o755))
	assert.NilError(t, os.Mkdir(workingDirectory, 0o755))
	projectKey, err := json.Marshal(project)
	assert.NilError(t, err)
	content := `{"theme":"dark","projects":{` + string(projectKey) + `:{"mcpServers":{"keep":{}},"hasTrustDialogAccepted":false}}}`

	updated, err := ClaudeTrustedConfig(content, []string{project, workingDirectory, project})
	assert.NilError(t, err)
	var configuration map[string]any
	assert.NilError(t, json.Unmarshal([]byte(updated), &configuration))
	assert.Equal(t, configuration["theme"], "dark")
	projects, ok := configuration[claudeProjectsKey].(map[string]any)
	assert.Assert(t, ok)
	projectValue, ok := projects[project].(map[string]any)
	assert.Assert(t, ok)
	assert.Equal(t, projectValue[claudeTrustKey], true)
	mcpServers, ok := projectValue["mcpServers"].(map[string]any)
	assert.Assert(t, ok)
	keepValue, ok := mcpServers["keep"].(map[string]any)
	assert.Assert(t, ok)
	assert.DeepEqual(t, keepValue, map[string]any{})
	workingValue, ok := projects[workingDirectory].(map[string]any)
	assert.Assert(t, ok)
	assert.Equal(t, workingValue[claudeTrustKey], true)

	repeated, err := ClaudeTrustedConfig(updated, []string{project, workingDirectory})
	assert.NilError(t, err)
	assert.Equal(t, repeated, updated)

	created, err := ClaudeTrustedConfig("", []string{project})
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(created, strconv.Quote(project)))
}

func TestClaudeTrustedConfigRejectsInvalidDocumentsAndTypes(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "malformed JSON", content: "{", want: invalidClaudeConfigMessage},
		{name: "scalar projects", content: `{"projects":[]}`, want: "Claude projects must be an object"},
		{name: "scalar project", content: `{"projects":{"` + project + `":[]}}`, want: "Claude project"},
		{name: "non-boolean trust", content: `{"projects":{"` + project + `":{"hasTrustDialogAccepted":"yes"}}}`, want: claudeBooleanTypeErrorMessage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ClaudeTrustedConfig(test.content, []string{project})
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestClaudeTrustPersistenceSerializesConcurrentUpdates(t *testing.T) {
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
			errors <- ensureClaudeDirectoryTrust(state, []string{project}, lockDirectory)
		}()
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		assert.NilError(t, err)
	}

	content, err := os.ReadFile(filepath.Join(state, claudeStateDirectoryName, claudeGlobalConfigName))
	assert.NilError(t, err)
	var configuration map[string]any
	assert.NilError(t, json.Unmarshal(content, &configuration))
	projectValues, ok := configuration[claudeProjectsKey].(map[string]any)
	assert.Assert(t, ok)
	for _, project := range projects {
		canonical, err := canonicalProjectPath(project)
		assert.NilError(t, err)
		projectValue, ok := projectValues[canonical].(map[string]any)
		assert.Assert(t, ok)
		assert.Equal(t, projectValue[claudeTrustKey], true)
	}
}

func TestClaudeTrustPersistenceAtomicallyReplacesAndPreservesMode(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	state := filepath.Join(root, "state")
	lockDirectory := filepath.Join(root, "locks")
	assert.NilError(t, os.Mkdir(project, 0o755))
	configDirectory := filepath.Join(state, claudeStateDirectoryName)
	assert.NilError(t, os.MkdirAll(configDirectory, 0o700))
	configPath := filepath.Join(configDirectory, claudeGlobalConfigName)
	content := `{"projects":{"` + project + `":{"hasTrustDialogAccepted":false}},"other":{"keep":true}}` + "\n"
	assert.NilError(t, os.WriteFile(configPath, []byte(content), 0o640))
	beforeInfo, err := os.Stat(configPath)
	assert.NilError(t, err)

	assert.NilError(t, ensureClaudeDirectoryTrust(state, []string{project}, lockDirectory))

	afterInfo, err := os.Stat(configPath)
	assert.NilError(t, err)
	assert.Assert(t, !os.SameFile(beforeInfo, afterInfo))
	assert.Equal(t, afterInfo.Mode().Perm(), os.FileMode(0o640))
	updated, err := os.ReadFile(configPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(updated), `"hasTrustDialogAccepted": true`))
	entries, err := os.ReadDir(configDirectory)
	assert.NilError(t, err)
	for _, entry := range entries {
		assert.Assert(t, !strings.HasPrefix(entry.Name(), claudeConfigTemporaryPrefix))
	}

	unchangedInfo, err := os.Stat(configPath)
	assert.NilError(t, err)
	assert.NilError(t, ensureClaudeDirectoryTrust(state, []string{project}, lockDirectory))
	finalInfo, err := os.Stat(configPath)
	assert.NilError(t, err)
	assert.Assert(t, os.SameFile(unchangedInfo, finalInfo))
}

func TestClaudeTrustPersistenceRejectsSymlinksAndUnsafeLockPaths(t *testing.T) {
	t.Run("configuration symlink", func(t *testing.T) {
		root := t.TempDir()
		project := filepath.Join(root, "project")
		state := filepath.Join(root, "state")
		outside := filepath.Join(root, "outside.json")
		lockDirectory := filepath.Join(root, "locks")
		assert.NilError(t, os.Mkdir(project, 0o755))
		configDirectory := filepath.Join(state, claudeStateDirectoryName)
		assert.NilError(t, os.MkdirAll(configDirectory, 0o700))
		assert.NilError(t, os.WriteFile(outside, []byte("keep\n"), 0o600))
		configPath := filepath.Join(configDirectory, claudeGlobalConfigName)
		assert.NilError(t, os.Symlink(outside, configPath))

		err := ensureClaudeDirectoryTrust(state, []string{project}, lockDirectory)
		assert.ErrorContains(t, err, "must not be a symlink")
		content, err := os.ReadFile(outside)
		assert.NilError(t, err)
		assert.Equal(t, string(content), "keep\n")
	})

	t.Run("lock path inside state", func(t *testing.T) {
		root := t.TempDir()
		project := filepath.Join(root, "project")
		state := filepath.Join(root, "state")
		assert.NilError(t, os.Mkdir(project, 0o755))
		assert.NilError(t, os.Mkdir(state, 0o700))

		err := ensureClaudeDirectoryTrust(state, []string{project}, filepath.Join(state, "locks"))
		assert.ErrorContains(t, err, "inside project or state mount")
		_, statErr := os.Stat(filepath.Join(state, claudeStateDirectoryName))
		assert.Assert(t, os.IsNotExist(statErr))
	})
}
