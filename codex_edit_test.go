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
	codexEditFixtureDirectory       = "testdata/codex-edit"
	codexEditPathPlaceholder        = "{{PROJECT_KEY}}"
	codexEditInsertedKey            = "{{EDIT_PROJECT_KEY}}"
	codexEditTrustedTOMLValue       = `"trusted"`
	codexDotsProjectKey             = "dots"
	codexQuotesProjectKey           = "quotes"
	codexBackslashProjectKey        = "backslash"
	codexUnicodeProjectKey          = "unicode"
	codexOrdinaryInputFixture       = "ordinary-replace/input.toml"
	codexOrdinaryExpectedFixture    = "ordinary-replace/expected.toml"
	codexTableInputFixture          = "table-insert/input.toml"
	codexTableExpectedFixture       = "table-insert/expected.toml"
	codexDottedInputFixture         = "dotted-insert/input.toml"
	codexDottedExpectedFixture      = "dotted-insert/expected.toml"
	codexInlineInputFixture         = "inline-insert/input.toml"
	codexInlineExpectedFixture      = "inline-insert/expected.toml"
	malformedTOMLTestName           = "malformed TOML"
	duplicateDefinitionTestName     = "duplicate definition"
	scalarProjectsTOML              = "projects = \"scalar\"\n"
	codexMalformedValue             = "trust_level =\n"
	codexDuplicateDefinitionContent = "trust_level = \"untrusted\"\ntrust_level = \"trusted\"\n"
)

func readCodexEditFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(codexEditFixtureDirectory, name))
	assert.NilError(t, err)
	return string(content)
}

func materializeCodexEditFixture(t *testing.T, name, project string) string {
	t.Helper()
	content := strings.ReplaceAll(readCodexEditFixture(t, name), codexEditPathPlaceholder, strconv.Quote(project))
	return strings.ReplaceAll(content, codexEditInsertedKey, "'"+project+"'")
}

func TestCodexEditPreservesGoldenFixtures(t *testing.T) {
	root := t.TempDir()
	projects := map[string]string{
		codexDotsProjectKey:      filepath.Join(root, "project.with.dots"),
		codexQuotesProjectKey:    filepath.Join(root, "quote\"dir"),
		codexBackslashProjectKey: filepath.Join(root, "back\\slash"),
		codexUnicodeProjectKey:   filepath.Join(root, "ユニコード"),
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
			project:    projects[codexDotsProjectKey],
			input:      codexOrdinaryInputFixture,
			expected:   codexOrdinaryExpectedFixture,
			pathExists: true,
		},
		{
			name:     "ordinary table insertion with quoted directory",
			project:  projects[codexQuotesProjectKey],
			input:    codexTableInputFixture,
			expected: codexTableExpectedFixture,
		},
		{
			name:     "dotted key insertion with backslash directory",
			project:  projects[codexBackslashProjectKey],
			input:    codexDottedInputFixture,
			expected: codexDottedExpectedFixture,
		},
		{
			name:     "inline table insertion with Unicode directory",
			project:  projects[codexUnicodeProjectKey],
			input:    codexInlineInputFixture,
			expected: codexInlineExpectedFixture,
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
		codexDotsProjectKey:      filepath.Join(root, "project.with.dots"),
		codexQuotesProjectKey:    filepath.Join(root, "quote\"dir"),
		codexBackslashProjectKey: filepath.Join(root, "back\\slash"),
		codexUnicodeProjectKey:   filepath.Join(root, "ユニコード"),
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
			project:  projects[codexDotsProjectKey],
			input:    codexOrdinaryInputFixture,
			expected: codexOrdinaryExpectedFixture,
		},
		{
			name:     "insert trust into ordinary table",
			project:  projects[codexQuotesProjectKey],
			input:    codexTableInputFixture,
			expected: codexTableExpectedFixture,
		},
		{
			name:     "insert trust into dotted project",
			project:  projects[codexBackslashProjectKey],
			input:    codexDottedInputFixture,
			expected: codexDottedExpectedFixture,
		},
		{
			name:     "insert trust into inline project",
			project:  projects[codexUnicodeProjectKey],
			input:    codexInlineInputFixture,
			expected: codexInlineExpectedFixture,
		},
		{
			name:     "create missing project",
			project:  projects[codexDotsProjectKey],
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
		{name: malformedTOMLTestName, content: codexMalformedValue, want: invalidCodexTOMLMessage},
		{name: duplicateDefinitionTestName, content: codexDuplicateDefinitionContent, want: invalidCodexTOMLMessage},
		{name: "projects scalar", content: scalarProjectsTOML, want: "Codex projects must be a table"},
		{name: "project scalar", content: "projects = { " + quotedProject + " = \"scalar\" }\n", want: "must be a table"},
		{name: "trust scalar", content: "[projects." + quotedProject + "]\ntrust_level = 1\n", want: stringTypeError},
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
	input := materializeCodexEditFixture(t, codexOrdinaryInputFixture, project)
	expected := materializeCodexEditFixture(t, codexOrdinaryExpectedFixture, project)
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
		{name: "malformed value", content: codexMalformedValue},
		{name: "duplicate key", content: codexDuplicateDefinitionContent},
		{name: "redefined table", content: "[projects]\n[projects]\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := tomledit.Parse([]byte(test.content))
			assert.Assert(t, err != nil)
		})
	}
}
