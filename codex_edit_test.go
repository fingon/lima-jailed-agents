package lja

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2/unstable"
	tomledit "github.com/pelletier/go-toml/v2/unstable/edit"
	"gotest.tools/v3/assert"
)

const (
	codexEditFixtureDirectory = "testdata/codex-edit"
	codexEditPathPlaceholder  = "{{PROJECT_KEY}}"
	codexEditInsertedKey      = "{{EDIT_PROJECT_KEY}}"
	codexEditTrustedTOMLValue = `"trusted"`
)

func readCodexEditFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(codexEditFixtureDirectory, name))
	assert.NilError(t, err)
	return string(content)
}

func materializeCodexEditFixture(t *testing.T, name string, project string) string {
	t.Helper()
	content := strings.ReplaceAll(readCodexEditFixture(t, name), codexEditPathPlaceholder, strconv.Quote(project))
	return strings.ReplaceAll(content, codexEditInsertedKey, "'"+project+"'")
}

func TestCodexEditPreservesGoldenFixtures(t *testing.T) {
	root := t.TempDir()
	projects := map[string]string{
		"dots":      filepath.Join(root, "project.with.dots"),
		"quotes":    filepath.Join(root, "quote\"dir"),
		"backslash": filepath.Join(root, "back\\slash"),
		"unicode":   filepath.Join(root, "ユニコード"),
	}
	for _, project := range projects {
		assert.NilError(t, os.Mkdir(project, 0o755))
	}

	tests := []struct {
		name       string
		project    string
		input      string
		expected   string
		pathExists bool
	}{
		{
			name:       "ordinary table replacement with dotted directory",
			project:    projects["dots"],
			input:      "ordinary-replace/input.toml",
			expected:   "ordinary-replace/expected.toml",
			pathExists: true,
		},
		{
			name:     "ordinary table insertion with quoted directory",
			project:  projects["quotes"],
			input:    "table-insert/input.toml",
			expected: "table-insert/expected.toml",
		},
		{
			name:     "dotted key insertion with backslash directory",
			project:  projects["backslash"],
			input:    "dotted-insert/input.toml",
			expected: "dotted-insert/expected.toml",
		},
		{
			name:     "inline table insertion with Unicode directory",
			project:  projects["unicode"],
			input:    "inline-insert/input.toml",
			expected: "inline-insert/expected.toml",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := materializeCodexEditFixture(t, test.input, test.project)
			expected := materializeCodexEditFixture(t, test.expected, test.project)
			document, err := tomledit.Parse([]byte(input))
			assert.NilError(t, err)
			assert.DeepEqual(t, document.Bytes(), []byte(input))

			path := []string{codexProjectsKey, test.project, codexTrustKey}
			before, present := document.Get(path)
			if test.pathExists {
				assert.Assert(t, present)
				assert.Equal(t, before, "untrusted")
			} else {
				assert.Assert(t, !present)
			}
			assert.NilError(t, document.Set(path, unstable.RawMessage(codexEditTrustedTOMLValue)))
			assert.Equal(t, document.String(), expected)

			value, present := document.Get(path)
			assert.Assert(t, present)
			assert.Equal(t, value, "trusted")
		})
	}
}

func TestCodexTrustedConfigPreservesGoldenFixtures(t *testing.T) {
	root := t.TempDir()
	projects := map[string]string{
		"dots":      filepath.Join(root, "project.with.dots"),
		"quotes":    filepath.Join(root, "quote\"dir"),
		"backslash": filepath.Join(root, "back\\slash"),
		"unicode":   filepath.Join(root, "ユニコード"),
	}
	for _, project := range projects {
		assert.NilError(t, os.Mkdir(project, 0o755))
	}
	tests := []struct {
		name     string
		project  string
		input    string
		expected string
	}{
		{
			name:     "replace ordinary trust setting",
			project:  projects["dots"],
			input:    "ordinary-replace/input.toml",
			expected: "ordinary-replace/expected.toml",
		},
		{
			name:     "insert trust into ordinary table",
			project:  projects["quotes"],
			input:    "table-insert/input.toml",
			expected: "table-insert/expected.toml",
		},
		{
			name:     "insert trust into dotted project",
			project:  projects["backslash"],
			input:    "dotted-insert/input.toml",
			expected: "dotted-insert/expected.toml",
		},
		{
			name:     "insert trust into inline project",
			project:  projects["unicode"],
			input:    "inline-insert/input.toml",
			expected: "inline-insert/expected.toml",
		},
		{
			name:     "create missing project",
			project:  projects["dots"],
			input:    "create-project/input.toml",
			expected: "create-project/expected.toml",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := materializeCodexEditFixture(t, test.input, test.project)
			expected := materializeCodexEditFixture(t, test.expected, test.project)
			updated, err := CodexTrustedConfig(input, []string{test.project})
			assert.NilError(t, err)
			assert.Equal(t, updated, expected)

			document, err := tomledit.Parse([]byte(updated))
			assert.NilError(t, err)
			value, present := document.Get(codexTrustPath(test.project))
			assert.Assert(t, present)
			assert.Equal(t, value, codexTrustedValue)

			repeated, err := CodexTrustedConfig(updated, []string{test.project})
			assert.NilError(t, err)
			assert.Equal(t, repeated, expected)
		})
	}
}

func TestCodexTrustedConfigUsesSemanticProjectPaths(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project.with.dots")
	assert.NilError(t, os.Mkdir(project, 0o755))
	quotedProject := strconv.Quote(project)
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "ordinary table",
			content: "[projects." + quotedProject + "]\n" +
				"trust_level = 'untrusted' # preserve this comment\n" +
				"other = true\n",
		},
		{
			name:    "dotted keys",
			content: "projects." + quotedProject + ".name = \"example\"\n",
		},
		{
			name:    "inline table",
			content: "projects = { " + quotedProject + " = { keep = \"yes\" } }\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updated, err := CodexTrustedConfig(test.content, []string{project, project})
			assert.NilError(t, err)
			assert.Assert(t, updated != test.content)

			document, err := tomledit.Parse([]byte(updated))
			assert.NilError(t, err)
			value, present := document.Get(codexTrustPath(project))
			assert.Assert(t, present)
			assert.Equal(t, value, codexTrustedValue)
		})
	}
}

func TestCodexTrustedConfigRejectsInvalidDocumentsAndTypes(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	quotedProject := strconv.Quote(project)
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "malformed TOML", content: "trust_level =\n", want: "invalid Codex TOML"},
		{name: "duplicate definition", content: "trust_level = \"untrusted\"\ntrust_level = \"trusted\"\n", want: "invalid Codex TOML"},
		{name: "projects scalar", content: "projects = \"scalar\"\n", want: "Codex projects must be a table"},
		{name: "project scalar", content: "projects = { " + quotedProject + " = \"scalar\" }\n", want: "must be a table"},
		{name: "trust scalar", content: "[projects." + quotedProject + "]\ntrust_level = 1\n", want: "must be a string"},
		{name: "unexpected trust value", content: "[projects." + quotedProject + "]\ntrust_level = \"maybe\"\n", want: "unsupported Codex trust value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CodexTrustedConfig(test.content, []string{project})
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestCodexEditPreservesCRLFAndMissingFinalNewline(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project.with.dots")
	assert.NilError(t, os.Mkdir(project, 0o755))
	input := materializeCodexEditFixture(t, "ordinary-replace/input.toml", project)
	expected := materializeCodexEditFixture(t, "ordinary-replace/expected.toml", project)
	input = strings.TrimSuffix(input, "\n")
	expected = strings.TrimSuffix(expected, "\n")
	input = strings.ReplaceAll(input, "\n", "\r\n")
	expected = strings.ReplaceAll(expected, "\n", "\r\n")

	updated, err := CodexTrustedConfig(input, []string{project})
	assert.NilError(t, err)
	assert.Equal(t, updated, expected)
}

func TestCodexEditRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "malformed value", content: "trust_level =\n"},
		{name: "duplicate key", content: "trust_level = \"untrusted\"\ntrust_level = \"trusted\"\n"},
		{name: "redefined table", content: "[projects]\n[projects]\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := tomledit.Parse([]byte(test.content))
			assert.Assert(t, err != nil)
		})
	}
}
