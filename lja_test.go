package lja

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

const (
	developmentConfigFixtureDirectory = "testdata/development-config"
	emptyTestName                     = "empty"
	buildCountEnvironment             = "BUILD_COUNT"
	buildEnabledEnvironment           = "BUILD_ENABLED"
	buildModeEnvironment              = "BUILD_MODE"
	emptyValueEnvironment             = "EMPTY_VALUE"
	projectOnlyEnvironment            = "PROJECT_ONLY"
	httpsProxyEnvironment             = "HTTPS_PROXY"
	agentTestName                     = "agent"
	exampleUserName                   = "Example"
	invalidTestError                  = "invalid"
	fallbackTokenValue                = "fallback"
	tokenEnvironment                  = "TOKEN"
	projectTokenEnvironment           = "PROJECT_TOKEN"
	gitUserNameKey                    = "user.name"
	testEnvironmentValue              = "test"
	stringTypeError                   = "must be a string"
	tokenArgument                     = "token"
	printfCommand                     = "printf"
	ninjaBuildPackage                 = "ninja-build"
	makeSetupScript                   = "make dep\nmake build\n"
	makeTestScript                    = "make test\n"
	unknownSettingTestName            = "unknown setting"
	wordWithSpaces                    = "word with spaces"
	targetWithSpaces                  = "target with spaces"
	prepareCommandName                = "prepare"
	literalTestValue                  = "literal value"
	globalOnlyConfigurationError      = "only allowed in global configuration"
	duplicateSettingTestName          = "duplicate setting"
	duplicateEnvironmentTestName      = "duplicate environment"
	nullEnvironmentValueTestName      = "null environment value"
	scalarCoercionTestName            = "scalar coercion"
	arrayScalarCoercionTestName       = "array scalar coercion"
	unknownAgentTestName              = "unknown agent"
	unknownLogicalToolName            = "unknown"
	multipleDocumentsTestName         = "multiple documents"
	opencodePromptValue               = "prompt"
	shellInjectionArgument            = "$(touch nope);"
	otherTestValue                    = "other"
	singleYAMLDocumentError           = "single YAML document"
	promptWithSpacesValue             = "prompt with spaces"
	opencodeRunCommand                = "run"
)

func readDevelopmentConfigFixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(developmentConfigFixtureDirectory, name))
	assert.NilError(t, err)
	return content
}

func TestProjectIdentityIsCanonical(t *testing.T) {
	temporary := t.TempDir()
	project := filepath.Join(temporary, "Project with spaces!")
	assert.NilError(t, os.Mkdir(project, 0o755))
	alias := filepath.Join(temporary, "alias")
	assert.NilError(t, os.Symlink(project, alias))

	canonical, err := CanonicalProjectPath(alias)
	assert.NilError(t, err)
	assert.Equal(t, canonical, project)
	firstName, err := ProjectVMName(project)
	assert.NilError(t, err)
	secondName, err := ProjectVMName(alias)
	assert.NilError(t, err)
	assert.Equal(t, firstName, secondName)
}

func TestProjectSlugUsesASCIIAndTwentyCharacters(t *testing.T) {
	project := filepath.Join(t.TempDir(), "Ä Strange_project! with a long name")
	slug, err := ProjectSlug(project)
	assert.NilError(t, err)
	assert.Equal(t, slug, "strange-project-wit")
}

func TestDevelopmentConfigurationLayeringAndEnvironment(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "host")
	configHome := filepath.Join(host, ".config")
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	globalPath := filepath.Join(configHome, "lja", globalConfigName)
	assert.NilError(t, os.MkdirAll(filepath.Dir(globalPath), 0o755))
	assert.NilError(t, os.WriteFile(globalPath, readDevelopmentConfigFixture(t, "layered/global.yaml"), 0o600))
	projectPath := filepath.Join(project, projectConfigName)
	assert.NilError(t, os.WriteFile(projectPath, readDevelopmentConfigFixture(t, "layered/project.yaml"), 0o600))

	config, err := loadDevelopmentConfig(project, map[string]string{xdgConfigHomeEnv: configHome}, host)
	assert.NilError(t, err)
	assert.DeepEqual(t, config.Packages, []string{ninjaBuildPackage})
	assert.DeepEqual(t, config.Agents, []string{openCodeAgentName})
	assert.Equal(t, config.CopyGitConfig, false)
	assert.DeepEqual(t, config.Env, map[string]string{
		buildCountEnvironment:   "2",
		buildEnabledEnvironment: guestConnectionProbe,
		buildModeEnvironment:    defaultProjectSlug,
		emptyValueEnvironment:   "",
		projectOnlyEnvironment:  configGitHubEnabled,
	})
	assert.DeepEqual(t, config.EnvPassthrough, []string{tokenEnvironment, httpsProxyEnvironment, projectTokenEnvironment})
	assert.Equal(t, len(config.Setup), 2)
	assert.DeepEqual(t, []string{config.Setup[0].Command, config.Setup[1].Command}, []string{makeSetupScript, makeTestScript})

	resolved, err := config.ResolveEnvironment(map[string]string{tokenEnvironment: literalTestValue, httpsProxyEnvironment: "", projectTokenEnvironment: otherTestValue})
	assert.NilError(t, err)
	assert.DeepEqual(t, resolved, map[string]string{
		tokenEnvironment: literalTestValue, httpsProxyEnvironment: "", projectTokenEnvironment: otherTestValue,
		buildCountEnvironment: "2", buildEnabledEnvironment: guestConnectionProbe, buildModeEnvironment: defaultProjectSlug, emptyValueEnvironment: "", projectOnlyEnvironment: configGitHubEnabled,
	})
}

func TestDevelopmentConfigurationProjectAgentsCanClearGlobalDefaults(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	configHome := filepath.Join(root, configDirectoryName)
	assert.NilError(t, os.Mkdir(project, 0o755))
	globalPath := filepath.Join(configHome, "lja", globalConfigName)
	assert.NilError(t, os.MkdirAll(filepath.Dir(globalPath), 0o755))
	assert.NilError(t, os.WriteFile(globalPath, []byte("agents: [codex, claude]\n"), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(project, projectConfigName), []byte("agents: []\n"), 0o600))

	config, err := loadDevelopmentConfig(project, map[string]string{xdgConfigHomeEnv: configHome}, root)
	assert.NilError(t, err)
	assert.DeepEqual(t, config.Agents, []string{})
}

func TestDevelopmentConfigurationTools(t *testing.T) {
	assert.DeepEqual(t, DefaultDevelopmentConfig().Tools, []string{})

	global, err := decodeDevelopmentConfig([]byte("tools: [prek, uv, prek]\n"), false)
	assert.NilError(t, err)
	assert.DeepEqual(t, global.Tools, []string{logicalToolPrek, logicalToolUV, logicalToolPrek})
	assert.Equal(t, global.HasTools, true)

	project, err := decodeDevelopmentConfig([]byte("tools: [uv, prek, uv]\n"), true)
	assert.NilError(t, err)
	config := applyDevelopmentConfig(DefaultDevelopmentConfig(), global, "global")
	config = applyDevelopmentConfig(config, project, "project")
	assert.DeepEqual(t, config.Tools, []string{logicalToolUV, logicalToolPrek})

	empty, err := decodeDevelopmentConfig([]byte("tools: []\n"), true)
	assert.NilError(t, err)
	config = applyDevelopmentConfig(config, empty, "project")
	assert.DeepEqual(t, config.Tools, []string{})

	cloned := config.clone()
	cloned.Tools = append(cloned.Tools, logicalToolPrek)
	assert.DeepEqual(t, config.Tools, []string{})
}

func TestDevelopmentConfigurationRejectsUnknownTools(t *testing.T) {
	err := ValidateDevelopmentConfig([]byte("tools: [unknown]\n"), true)
	assert.ErrorContains(t, err, "invalid tool")
}

func TestDevelopmentConfigurationRejectsManagedEnvironment(t *testing.T) {
	err := ValidateDevelopmentConfig([]byte(`{"env":{"CODEX_HOME":"/tmp/state"}}`), true)
	assert.ErrorContains(t, err, "managed by LJA")
	err = ValidateDevelopmentConfig([]byte(`{"packages":["--help"]}`), true)
	assert.ErrorContains(t, err, "invalid package name")
}

func TestDevelopmentConfigurationAcceptsYAMLAndPreservesPresence(t *testing.T) {
	content := []byte(`
# A YAML comment is not part of the command.
packages:
  - git
  - make
agents:
  - codex
  - claude
copy_git_config: false
env:
  BUILD_MODE: development
  BUILD_COUNT: "3"
env_passthrough:
  - TOKEN
setup:
  - |
    make dep
    make build
inherit_setup: false
inherit_env_passthrough: true
`)
	decoded, err := decodeDevelopmentConfig(content, true)
	assert.NilError(t, err)
	assert.DeepEqual(t, decoded.Packages, []string{gitCommand, makeCommand})
	assert.Equal(t, decoded.HasPackages, true)
	assert.DeepEqual(t, decoded.Agents, []string{codexAgentName, claudeAgentName})
	assert.Equal(t, decoded.HasAgents, true)
	assert.Equal(t, decoded.CopyGitConfig, false)
	assert.Equal(t, decoded.HasCopyGitConfig, true)
	assert.DeepEqual(t, decoded.Env, map[string]string{buildModeEnvironment: "development", buildCountEnvironment: "3"})
	assert.Equal(t, decoded.HasEnv, true)
	assert.DeepEqual(t, decoded.EnvPassthrough, []string{tokenEnvironment})
	assert.Equal(t, decoded.HasEnvPassthrough, true)
	assert.DeepEqual(t, decoded.Setup, []string{makeSetupScript})
	assert.Equal(t, decoded.HasSetup, true)
	assert.Equal(t, decoded.InheritSetup, false)
	assert.Equal(t, decoded.HasInheritSetup, true)
	assert.Equal(t, decoded.InheritEnvironmentPassthrough, true)
	assert.Equal(t, decoded.HasInheritEnvironmentPassthrough, true)

	decoded, err = decodeDevelopmentConfig([]byte("{}\n"), true)
	assert.NilError(t, err)
	assert.Equal(t, decoded.HasPackages, false)
	assert.Equal(t, decoded.HasAgents, false)
	assert.Equal(t, decoded.HasCopyGitConfig, false)
	assert.Equal(t, decoded.HasEnv, false)
	assert.Equal(t, decoded.HasEnvPassthrough, false)
	assert.Equal(t, decoded.HasSetup, false)
}

func TestGitHubConfigurationLayeringAndClone(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	configHome := filepath.Join(root, configDirectoryName)
	assert.NilError(t, os.Mkdir(project, 0o755))
	globalPath := filepath.Join(configHome, "lja", globalConfigName)
	assert.NilError(t, os.MkdirAll(filepath.Dir(globalPath), 0o755))
	assert.NilError(t, os.WriteFile(globalPath, []byte("github:\n  enabled: true\n  token_command: [gh, auth, token]\n"), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(project, projectConfigName), []byte("github:\n  enabled: false\n"), 0o600))

	config, err := loadDevelopmentConfig(project, map[string]string{xdgConfigHomeEnv: configHome}, root)
	assert.NilError(t, err)
	assert.Equal(t, config.GitHub.Enabled, false)
	assert.DeepEqual(t, config.GitHub.TokenCommand, []string{ghCommand, claudeAuthSubcommand, tokenArgument})

	cloned := config.clone()
	cloned.GitHub.TokenCommand[0] = "changed"
	assert.Equal(t, config.GitHub.TokenCommand[0], "gh")
	assert.DeepEqual(t, config.AsYAML().GitHub.TokenCommand, []string{ghCommand, claudeAuthSubcommand, tokenArgument})
}

func TestGitHubConfigurationValidation(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		isProject bool
		wantError string
	}{
		{name: "empty global mapping", content: "github: {}\n"},
		{name: "global command", content: "github:\n  enabled: true\n  token_command: [gh, auth, token]\n"},
		{name: "project enabled", content: "github:\n  enabled: true\n", isProject: true},
		{name: "project empty command", content: "github:\n  token_command: []\n", isProject: true, wantError: globalOnlyConfigurationError},
		{name: "project command", content: "github:\n  token_command: [gh]\n", isProject: true, wantError: globalOnlyConfigurationError},
		{name: unknownSettingTestName, content: "github:\n  host: github.com\n", wantError: "unknown github settings"},
		{name: "null mapping", content: "github: null\n", wantError: "github must not be null"},
		{name: "invalid enabled type", content: "github:\n  enabled: \"true\"\n", wantError: "github.enabled must be a boolean"},
		{name: "invalid command type", content: "github:\n  token_command: gh\n", wantError: "github.token_command must be an array"},
		{name: "empty command argument", content: "github:\n  token_command: [\"\"]\n", wantError: "github.token_command must be an array of nonempty strings"},
		{name: "explicit token environment", content: "github:\n  enabled: true\nenv:\n  GH_TOKEN: value\n", wantError: "GH_TOKEN cannot be configured"},
		{name: "fallback token environment", content: "github:\n  enabled: true\nenv:\n  GITHUB_TOKEN: value\n", wantError: "GITHUB_TOKEN cannot be configured"},
		{name: "token passthrough", content: "github:\n  enabled: true\nenv_passthrough: [GH_TOKEN]\n", wantError: "GH_TOKEN cannot be passed through"},
		{name: "disabled manual environment", content: "github:\n  enabled: false\nenv:\n  GH_TOKEN: value\nenv_passthrough: [GITHUB_TOKEN]\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDevelopmentConfig([]byte(test.content), test.isProject)
			if test.wantError == "" {
				assert.NilError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestDevelopmentConfigurationRejectsInvalidYAML(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: unknownSettingTestName, content: []byte("unknown: true\n"), want: "unknown settings: unknown"},
		{name: duplicateSettingTestName, content: []byte("packages: []\npackages: []\n"), want: "duplicate configuration key: packages"},
		{name: duplicateEnvironmentTestName, content: []byte("env:\n  BUILD_MODE: one\n  BUILD_MODE: two\n"), want: "duplicate env key: BUILD_MODE"},
		{name: "null setting", content: []byte("packages: null\n"), want: "packages must not be null"},
		{name: nullEnvironmentValueTestName, content: []byte("env:\n  BUILD_MODE: null\n"), want: "env must be a mapping with string values"},
		{name: scalarCoercionTestName, content: []byte("copy_git_config: \"true\"\n"), want: "copy_git_config must be a boolean"},
		{name: arrayScalarCoercionTestName, content: []byte("packages: [git, true]\n"), want: "packages must be a string"},
		{name: unknownAgentTestName, content: []byte("agents: [unknown]\n"), want: unknownAgentTestName},
		{name: "root sequence", content: []byte("- git\n"), want: "configuration must be a mapping"},
		{name: multipleDocumentsTestName, content: []byte("{}\n---\n{}\n"), want: singleYAMLDocumentError},
		{name: "empty input", content: []byte("# comment only\n"), want: "must contain a YAML document"},
		{name: "invalid UTF-8", content: []byte{'p', 'a', 'c', 'k', 'a', 'g', 'e', 's', ':', ' ', 0xff}, want: "not valid UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDevelopmentConfig(test.content, true)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestDevelopmentConfigurationErrorsIncludeSourceAndLocation(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	configHome := filepath.Join(root, configDirectoryName)
	assert.NilError(t, os.Mkdir(project, 0o755))
	globalPath := filepath.Join(configHome, "lja", globalConfigName)
	assert.NilError(t, os.MkdirAll(filepath.Dir(globalPath), 0o755))
	assert.NilError(t, os.WriteFile(globalPath, []byte("packages:\n  - true\n"), 0o600))

	_, err := loadDevelopmentConfig(project, map[string]string{xdgConfigHomeEnv: configHome}, root)
	assert.ErrorContains(t, err, globalPath)
	assert.ErrorContains(t, err, "line 2")
	assert.ErrorContains(t, err, "column 5")
}

func TestDevelopmentConfigurationIgnoresLegacyJSONFiles(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	configHome := filepath.Join(root, configDirectoryName)
	assert.NilError(t, os.Mkdir(project, 0o755))
	legacyGlobalPath := filepath.Join(configHome, "lja", "config.json")
	assert.NilError(t, os.MkdirAll(filepath.Dir(legacyGlobalPath), 0o755))
	assert.NilError(t, os.WriteFile(legacyGlobalPath, []byte(`{"packages":["ninja-build"]}`), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(project, ".lja.json"), []byte(`{"packages":["ninja-build"]}`), 0o600))

	config, err := loadDevelopmentConfig(project, map[string]string{xdgConfigHomeEnv: configHome}, root)
	assert.NilError(t, err)
	assert.DeepEqual(t, config.Packages, []string{gitCommand, makeCommand})
	assert.Equal(t, config.CopyGitConfig, true)
}

func TestDevelopmentConfigurationFilenames(t *testing.T) {
	assert.Equal(t, globalConfigName, "config.yaml")
	assert.Equal(t, projectConfigName, ".lja.yaml")
	assert.Equal(t, ProjectConfigName, ".lja.yaml")
}

func TestProjectDiscoveryUsesYAMLMarker(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	assert.NilError(t, os.MkdirAll(nested, 0o755))
	limactlCommand := filepath.Join(root, "fake-limactl")
	assert.NilError(t, os.WriteFile(limactlCommand, []byte("#!/bin/sh\nprintf '%s\\n' '[]'\n"), 0o755))

	assert.NilError(t, os.WriteFile(filepath.Join(project, projectConfigName), []byte("{}\n"), 0o600))
	discovered, err := DiscoverProject(nested, limactlCommand)
	assert.NilError(t, err)
	assert.Equal(t, discovered, project)

	assert.NilError(t, os.Remove(filepath.Join(project, projectConfigName)))
	assert.NilError(t, os.WriteFile(filepath.Join(project, ".lja.json"), []byte("{}\n"), 0o600))
	discovered, err = DiscoverProject(nested, limactlCommand)
	assert.NilError(t, err)
	assert.Equal(t, discovered, nested)
}

func TestExplicitProjectSelectionUsesExactDirectory(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "selected")
	workingDirectory := filepath.Join(selected, "working")
	assert.NilError(t, os.MkdirAll(workingDirectory, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(selected, projectConfigName), []byte("packages: [git]\n"), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(workingDirectory, projectConfigName), []byte("packages: [ninja-build]\n"), 0o600))

	project, err := ResolveProject(selected, workingDirectory)
	assert.NilError(t, err)
	assert.Equal(t, project, selected)
	config, err := loadDevelopmentConfig(project, map[string]string{xdgConfigHomeEnv: filepath.Join(root, configDirectoryName)}, root)
	assert.NilError(t, err)
	assert.DeepEqual(t, config.Packages, []string{gitCommand})
}

func TestDevelopmentConfigurationYAMLOutputIsDeterministic(t *testing.T) {
	config := DevelopmentConfig{
		Packages:      []string{makeCommand, gitCommand},
		Tools:         []string{logicalToolPrek, logicalToolUV},
		Agents:        []string{claudeAgentName},
		CopyGitConfig: false,
		Env: map[string]string{
			"Z_LAST":  "last",
			"A_FIRST": "first",
		},
		EnvPassthrough: []string{tokenEnvironment},
		Setup:          []SetupCommand{{Source: "/private/source.yaml", Index: 7, Command: "make dep\nmake build\n"}},
	}
	want := "gpg_forwarding: false\ngithub:\n  enabled: false\n  token_command: []\nlima: {}\npackages:\n  - make\n  - git\ntools:\n  - prek\n  - uv\nagents:\n  - claude\ncopy_git_config: false\nenv:\n  A_FIRST: first\n  Z_LAST: last\nenv_passthrough:\n  - TOKEN\nsetup:\n  - |\n    make dep\n    make build\n"
	encoded, err := yamlConfiguration(config)
	assert.NilError(t, err)
	assert.Equal(t, string(encoded), want)
	assert.Assert(t, !strings.Contains(string(encoded), "private/source.yaml"))

	repeated, err := yamlConfiguration(config)
	assert.NilError(t, err)
	assert.DeepEqual(t, repeated, encoded)

	empty, err := yamlConfiguration(DefaultDevelopmentConfig())
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(empty), "env: {}\n"))
	assert.Assert(t, strings.Contains(string(empty), "env_passthrough: []\n"))
	assert.Assert(t, strings.Contains(string(empty), "agents: []\n"))
	assert.Assert(t, strings.Contains(string(empty), "tools: []\n"))
	assert.Assert(t, strings.Contains(string(empty), "setup: []\n"))
	assert.Assert(t, strings.HasSuffix(string(empty), "\n"))
}

func TestDevelopmentConfigurationFixtures(t *testing.T) {
	tests := []struct {
		name              string
		globalFixture     string
		projectFixture    string
		expectedFixture   string
		wantGitHubCommand []string
		wantGitHubEnabled bool
		wantPackages      []string
		wantAgents        []string
		wantCopyGitConfig bool
		wantEnvironment   map[string]string
		wantPassthrough   []string
		wantSetup         []string
		wantSetupIndices  []int
	}{
		{
			name:              "missing files",
			expectedFixture:   "missing/expected.yaml",
			wantPackages:      []string{gitCommand, makeCommand},
			wantAgents:        []string{},
			wantCopyGitConfig: true,
			wantEnvironment:   map[string]string{},
			wantPassthrough:   []string{},
			wantSetup:         []string{},
			wantSetupIndices:  []int{},
		},
		{
			name:              "empty mappings",
			globalFixture:     "empty/global.yaml",
			projectFixture:    "empty/project.yaml",
			expectedFixture:   "empty/expected.yaml",
			wantPackages:      []string{gitCommand, makeCommand},
			wantAgents:        []string{},
			wantCopyGitConfig: true,
			wantEnvironment:   map[string]string{},
			wantPassthrough:   []string{},
			wantSetup:         []string{},
			wantSetupIndices:  []int{},
		},
		{
			name:              "explicit empty settings",
			projectFixture:    "explicit-empty/project.yaml",
			expectedFixture:   "explicit-empty/expected.yaml",
			wantPackages:      []string{},
			wantAgents:        []string{},
			wantCopyGitConfig: true,
			wantEnvironment:   map[string]string{},
			wantPassthrough:   []string{},
			wantSetup:         []string{},
			wantSetupIndices:  []int{},
		},
		{
			name:              "global and project precedence",
			globalFixture:     "layered/global.yaml",
			projectFixture:    "layered/project.yaml",
			expectedFixture:   "layered/expected.yaml",
			wantGitHubCommand: []string{ghCommand, claudeAuthSubcommand, tokenArgument, "--hostname", "github.com"},
			wantPackages:      []string{ninjaBuildPackage},
			wantAgents:        []string{openCodeAgentName},
			wantCopyGitConfig: false,
			wantEnvironment: map[string]string{
				buildCountEnvironment:   "2",
				buildEnabledEnvironment: guestConnectionProbe,
				buildModeEnvironment:    defaultProjectSlug,
				emptyValueEnvironment:   "",
				projectOnlyEnvironment:  configGitHubEnabled,
			},
			wantPassthrough:  []string{tokenEnvironment, httpsProxyEnvironment, projectTokenEnvironment},
			wantSetup:        []string{makeSetupScript, "make test\n"},
			wantSetupIndices: []int{1, 1},
		},
		{
			name:              "project enables inherited GitHub command",
			globalFixture:     "github-enabled/global.yaml",
			projectFixture:    "github-enabled/project.yaml",
			expectedFixture:   "github-enabled/expected.yaml",
			wantGitHubCommand: []string{ghCommand, claudeAuthSubcommand, tokenArgument},
			wantGitHubEnabled: true,
			wantPackages:      []string{gitCommand, makeCommand},
			wantAgents:        []string{},
			wantCopyGitConfig: true,
			wantEnvironment:   map[string]string{},
			wantPassthrough:   []string{},
			wantSetup:         []string{},
			wantSetupIndices:  []int{},
		},
		{
			name:              "inheritance switches",
			globalFixture:     "inherit-disabled/global.yaml",
			projectFixture:    "inherit-disabled/project.yaml",
			expectedFixture:   "inherit-disabled/expected.yaml",
			wantPackages:      []string{gitCommand, makeCommand},
			wantAgents:        []string{},
			wantCopyGitConfig: true,
			wantEnvironment:   map[string]string{},
			wantPassthrough:   []string{projectTokenEnvironment},
			wantSetup:         []string{"project setup"},
			wantSetupIndices:  []int{1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "project")
			configHome := filepath.Join(root, configDirectoryName)
			assert.NilError(t, os.Mkdir(project, 0o755))
			if test.globalFixture != "" {
				globalPath := filepath.Join(configHome, "lja", globalConfigName)
				assert.NilError(t, os.MkdirAll(filepath.Dir(globalPath), 0o755))
				assert.NilError(t, os.WriteFile(globalPath, readDevelopmentConfigFixture(t, test.globalFixture), 0o600))
			}
			if test.projectFixture != "" {
				projectPath := filepath.Join(project, projectConfigName)
				assert.NilError(t, os.WriteFile(projectPath, readDevelopmentConfigFixture(t, test.projectFixture), 0o600))
			}

			config, err := loadDevelopmentConfig(project, map[string]string{xdgConfigHomeEnv: configHome}, root)
			assert.NilError(t, err)
			assert.DeepEqual(t, config.Packages, test.wantPackages)
			assert.DeepEqual(t, config.Agents, test.wantAgents)
			assert.Equal(t, config.GitHub.Enabled, test.wantGitHubEnabled)
			assert.DeepEqual(t, config.GitHub.TokenCommand, append([]string{}, test.wantGitHubCommand...))
			assert.Equal(t, config.CopyGitConfig, test.wantCopyGitConfig)
			assert.DeepEqual(t, config.Env, test.wantEnvironment)
			assert.DeepEqual(t, config.EnvPassthrough, test.wantPassthrough)
			assert.Equal(t, len(config.Setup), len(test.wantSetup))
			for index, command := range test.wantSetup {
				assert.Equal(t, config.Setup[index].Command, command)
				assert.Equal(t, config.Setup[index].Index, test.wantSetupIndices[index])
			}

			encoded, err := yamlConfiguration(config)
			assert.NilError(t, err)
			expected := readDevelopmentConfigFixture(t, test.expectedFixture)
			assert.Equal(t, string(encoded), string(expected))
		})
	}
}

func TestDevelopmentConfigurationFixtureValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    string
	}{
		{name: "unknown setting", fixture: "invalid/unknown.yaml", want: "unknown settings"},
		{name: duplicateSettingTestName, fixture: "invalid/duplicate.yaml", want: "duplicate configuration key"},
		{name: duplicateEnvironmentTestName, fixture: "invalid/duplicate-env.yaml", want: "duplicate env key"},
		{name: "null value", fixture: "invalid/null.yaml", want: nullValueError},
		{name: nullEnvironmentValueTestName, fixture: "invalid/null-environment.yaml", want: "must be a mapping with string values"},
		{name: scalarCoercionTestName, fixture: "invalid/scalar-coercion.yaml", want: "must be a boolean"},
		{name: arrayScalarCoercionTestName, fixture: "invalid/array-scalar-coercion.yaml", want: stringTypeError},
		{name: multipleDocumentsTestName, fixture: "invalid/multiple-documents.yaml", want: singleYAMLDocumentError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDevelopmentConfig(readDevelopmentConfigFixture(t, test.fixture), true)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestStateRootPrecedenceAndOverlap(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	state := filepath.Join(root, "state")
	assert.NilError(t, os.Mkdir(project, 0o755))
	selected, err := ResolveStateRoot(project, nil, false, map[string]string{stateDirectoryEnv: state})
	assert.NilError(t, err)
	canonicalState, err := canonicalPath(state)
	assert.NilError(t, err)
	assert.Equal(t, selected, canonicalState)
	projectState, err := ResolveStateRoot(project, &state, true, map[string]string{stateDirectoryEnv: filepath.Join(root, "other")})
	assert.NilError(t, err)
	canonicalProject, err := canonicalPath(project)
	assert.NilError(t, err)
	assert.Equal(t, projectState, canonicalProject)

	_, err = ResolveStateRoot(project, nil, false, map[string]string{stateDirectoryEnv: filepath.Join(project, "nested")})
	assert.ErrorContains(t, err, "overlaps project")
}

func TestLimaJSONLinesAndExactMounts(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	vmName, err := ProjectVMName(project)
	assert.NilError(t, err)
	payload := strings.Join([]string{
		`{"name":"first","status":"Running","config":{"mounts":[]}}`,
		`{"name":"second","status":"Stopped","config":{"mounts":[]}}`,
	}, "\n")
	instances, err := ParseLimaInstances([]byte(payload))
	assert.NilError(t, err)
	assert.Equal(t, len(instances), 2)
	assert.Equal(t, instances[0].Name, "first")
	assert.Equal(t, instances[1].Status, "Stopped")

	mounts := LimaInstance{Name: vmName, Status: limaStatusRunning, Mounts: []LimaMount{{Location: project, MountPoint: project, Writable: true}}}
	assert.NilError(t, ValidateProjectMount(mounts, project, ""))
	mountArguments, err := MountArguments([]string{project + ",quote"})
	assert.NilError(t, err)
	assert.Equal(t, mountArguments[0], "--mount-only")
	assert.Assert(t, strings.Contains(mountArguments[1], `"`))
}

func TestCodexTrustEditorPreservesSettingsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	content := "# keep\nmodel = \"example\"\n[projects.\"" + project + "\"]\ntrust_level = \"untrusted\" # comment\nother = true\n"
	updated, err := CodexTrustedConfig(content, []string{project, project})
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(updated, `trust_level = "trusted" # comment`))
	assert.Assert(t, strings.Contains(updated, `[projects."`+project+`"]`))
	repeated, err := CodexTrustedConfig(updated, []string{project})
	assert.NilError(t, err)
	assert.Equal(t, repeated, updated)

	incompatible := scalarProjectsTOML
	_, err = CodexTrustedConfig(incompatible, []string{project})
	assert.ErrorContains(t, err, "Codex projects must be a table")
}

func TestCodexTrustPersistenceUsesPrivateModeAndNoUnnecessaryWrite(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	state := filepath.Join(root, "state")
	assert.NilError(t, os.Mkdir(state, 0o755))
	assert.NilError(t, ensureCodexDirectoryTrust(state, []string{project}, filepath.Join(root, "locks")))
	configPath := filepath.Join(state, codexStateDirectoryName, codexConfigName)
	info, err := os.Stat(configPath)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
	before, err := os.ReadFile(configPath)
	assert.NilError(t, err)
	beforeInfo, err := os.Stat(configPath)
	assert.NilError(t, err)
	assert.NilError(t, ensureCodexDirectoryTrust(state, []string{project}, filepath.Join(root, "locks")))
	after, err := os.ReadFile(configPath)
	assert.NilError(t, err)
	assert.DeepEqual(t, after, before)
	afterInfo, err := os.Stat(configPath)
	assert.NilError(t, err)
	assert.Assert(t, os.SameFile(beforeInfo, afterInfo))
}

func TestAgentInvocationAndWrappers(t *testing.T) {
	arguments, environment, err := BuildAgentInvocation("codex", []string{codeXExecSubcommand, promptWithSpacesValue})
	assert.NilError(t, err)
	assert.DeepEqual(t, arguments, []string{codeXExecSubcommand, codeXPermissionFlag, promptWithSpacesValue})
	assert.Equal(t, len(environment), 0)
	arguments, _, err = BuildAgentInvocation(claudeAgentName, []string{claudeAuthSubcommand, codeXLoginSubcommand})
	assert.NilError(t, err)
	assert.DeepEqual(t, arguments, []string{claudeAuthSubcommand, codeXLoginSubcommand})
	arguments, environment, err = BuildAgentInvocation("opencode", []string{opencodeRunCommand, opencodePromptValue})
	assert.NilError(t, err)
	assert.DeepEqual(t, arguments, []string{opencodeRunCommand, opencodePromptValue})
	assert.Equal(t, environment[openCodeConfigContentEnvironment], openCodePermissionConfig)

	state := filepath.Join(t.TempDir(), "state")
	content, err := AgentWrapperContent(state, "codex", "/usr/bin/codex")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(content, "unset "+codeXHomeEnvironment))
	assert.Assert(t, strings.Contains(content, codeXPermissionFlag))
	assert.Assert(t, strings.Contains(content, codeXLoginSubcommand))

	script, err := agentWrapperSetupScript(state, "test-vm", []AgentExecutable{{Name: codexAgentName, Path: "/usr/bin/codex"}})
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(script, "rm -f -- '/tmp/lja/test-vm/bin/claude'"))
	assert.Assert(t, !strings.Contains(script, "rm -f -- '/tmp/lja/test-vm/bin/codex'"))
}

func TestConfiguredAgentNames(t *testing.T) {
	config := &DevelopmentConfig{Agents: []string{claudeAgentName, codexAgentName, claudeAgentName}}
	configured, err := configuredAgentNames(config)
	assert.NilError(t, err)
	assert.DeepEqual(t, configured, []string{claudeAgentName, codexAgentName})

	effective, err := effectiveAgentNames(codexAgentName, []string{openCodeAgentName, claudeAgentName}, config)
	assert.NilError(t, err)
	assert.DeepEqual(t, effective, []string{codexAgentName, claudeAgentName, openCodeAgentName})

	_, err = configuredAgentNames(&DevelopmentConfig{Agents: []string{unknownLogicalToolName}})
	assert.ErrorContains(t, err, unknownAgentTestName)
}

func TestConfiguredAgentsAreAvailableToShell(t *testing.T) {
	project, vmName, options := vmFixture(t, "")
	config := DevelopmentConfig{Agents: []string{codexAgentName, claudeAgentName}}
	options.Development = &config

	code, err := OpenShell(project, []string{echoCommand, agentTestName}, project, options)
	assert.NilError(t, err)
	assert.Equal(t, code, 0)

	database, err := readVMDatabase()
	assert.NilError(t, err)
	var wrapperScript string
	for _, operation := range database.Operations {
		if len(operation) > 0 && operation[len(operation)-1] != agentTestName && strings.Contains(strings.Join(operation, " "), agentWrapperDirectory(vmName)) {
			wrapperScript = operation[len(operation)-1]
		}
	}
	assert.Assert(t, strings.Contains(wrapperScript, agentWrapperDirectory(vmName)+"/codex"))
	assert.Assert(t, strings.Contains(wrapperScript, agentWrapperDirectory(vmName)+"/claude"))

	last := database.Operations[len(database.Operations)-1]
	assert.Assert(t, strings.Contains(last[10], "PATH='"+agentWrapperDirectory(vmName)+"':${PATH-}"))
	assert.DeepEqual(t, last[12:], []string{echoCommand, agentTestName})
}

func TestCreateEnsuresConfiguredAgentsOnExistingVM(t *testing.T) {
	for _, status := range []string{limaStatusRunning, limaStatusStopped} {
		t.Run(status, func(t *testing.T) {
			project, vmName, options := vmFixture(t, status)
			config := DevelopmentConfig{Agents: []string{codexAgentName}}
			options.Development = &config

			instance, err := CreateVM(project, options)
			assert.NilError(t, err)
			assert.Equal(t, instance.Status, limaStatusRunning)

			database, err := readVMDatabase()
			assert.NilError(t, err)
			for _, operation := range database.Operations {
				assert.Assert(t, operation[0] != testSetupCommand)
			}
			expectedOperations := 3
			if status == limaStatusStopped {
				assert.Equal(t, database.Operations[0][0], "start")
				expectedOperations++
			}
			assert.Equal(t, len(database.Operations), expectedOperations)
			assert.Assert(t, strings.Contains(strings.Join(database.Operations[len(database.Operations)-1], " "), agentWrapperDirectory(vmName)))
		})
	}
}

func TestGuestProcessPreservesArgumentAndEnvironmentBoundaries(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	logPath := filepath.Join(root, "arguments.log")
	commandPath := filepath.Join(root, "fake-limactl")
	command := "#!/bin/sh\nprintf '%s\\0' \"$@\" > " + shellQuote(logPath) + "\nexit 23\n"
	assert.NilError(t, os.WriteFile(commandPath, []byte(command), 0o755))

	options := defaultProcessOptions(commandPath)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, "lja-test", []string{printfCommand, shellInjectionArgument, wordWithSpaces}, guestExecution(options, map[string]string{
		"Z_VALUE": "value with spaces",
		"A_VALUE": "literal;value",
	}))
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 23)
	encoded, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	actual := strings.Split(strings.TrimSuffix(string(encoded), "\x00"), "\x00")
	assert.DeepEqual(t, actual, []string{
		limaShellOperation,
		limaWorkdirFlag,
		project,
		"lja-test",
		"A_VALUE=literal;value",
		"Z_VALUE=value with spaces",
		shellCommand,
		shellCommandFlag,
		guestPathBootstrapScript,
		programName,
		printfCommand,
		shellInjectionArgument,
		wordWithSpaces,
	})

	_, err = runGuest(project, "lja-test", []string{"true"}, guestExecution(options, map[string]string{"BAD": "contains\x00nul"}))
	assert.ErrorContains(t, err, "contains a NUL")
}

func TestLimaProcessInheritsStdin(t *testing.T) {
	commandPath := filepath.Join(t.TempDir(), "fake-limactl")
	command := "#!/bin/sh\nIFS= read -r value\nprintf '%s' \"$value\"\n"
	assert.NilError(t, os.WriteFile(commandPath, []byte(command), 0o755))

	stdinReader, stdinWriter, err := os.Pipe()
	assert.NilError(t, err)
	originalStdin := os.Stdin
	os.Stdin = stdinReader
	t.Cleanup(func() {
		os.Stdin = originalStdin
		assert.NilError(t, stdinReader.Close())
	})
	_, err = stdinWriter.WriteString("forwarded\n")
	assert.NilError(t, err)
	assert.NilError(t, stdinWriter.Close())

	options := defaultProcessOptions(commandPath)
	options.captureOutput = true
	result, err := runLima([]string{"read-stdin"}, options)
	assert.NilError(t, err)
	assert.Equal(t, string(result.Stdout), "forwarded")
}

func TestInstructionRefreshIsAtomicAndOptional(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "host")
	state := filepath.Join(root, "state")
	assert.NilError(t, os.MkdirAll(filepath.Join(host, "codex"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(host, "codex", "AGENTS.md"), []byte("first\n"), 0o600))
	_, err := EnsureAgentStateDirectories(state, "codex")
	assert.NilError(t, err)

	changed, err := RefreshAgentInstructions(state, "codex", map[string]string{
		codeXHomeEnvironment: filepath.Join(host, "codex"),
	})
	assert.NilError(t, err)
	assert.Equal(t, changed, true)
	_, destination, err := AgentInstructionPaths(state, "codex", map[string]string{
		codeXHomeEnvironment: filepath.Join(host, "codex"),
	})
	assert.NilError(t, err)
	content, err := os.ReadFile(destination)
	assert.NilError(t, err)
	assert.Equal(t, string(content), "first\n")
	info, err := os.Stat(destination)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))

	assert.NilError(t, os.WriteFile(filepath.Join(host, "codex", "AGENTS.md"), []byte("second\n"), 0o600))
	changed, err = RefreshAgentInstructions(state, "codex", map[string]string{
		codeXHomeEnvironment: filepath.Join(host, "codex"),
	})
	assert.NilError(t, err)
	assert.Equal(t, changed, true)
	content, err = os.ReadFile(destination)
	assert.NilError(t, err)
	assert.Equal(t, string(content), "second\n")

	assert.NilError(t, os.Remove(filepath.Join(host, "codex", "AGENTS.md")))
	changed, err = RefreshAgentInstructions(state, "codex", map[string]string{
		codeXHomeEnvironment: filepath.Join(host, "codex"),
	})
	assert.NilError(t, err)
	assert.Equal(t, changed, false)
	content, err = os.ReadFile(destination)
	assert.NilError(t, err)
	assert.Equal(t, string(content), "second\n")
}

func TestAgentLaunchUsesValidatedVMAndRetryableGuestInstallation(t *testing.T) {
	for _, test := range []struct {
		name               string
		nodeInitiallyReady bool
		npmInitiallyReady  bool
		wantNativeInstall  bool
	}{
		{name: "absent runtime", wantNativeInstall: true},
		{name: "partial runtime with node", nodeInitiallyReady: true, wantNativeInstall: true},
		{name: "partial runtime with npm", npmInitiallyReady: true, wantNativeInstall: true},
		{name: "working custom runtime", nodeInitiallyReady: true, npmInitiallyReady: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "project")
			assert.NilError(t, os.Mkdir(project, 0o755))
			vmName, err := ProjectVMName(project)
			assert.NilError(t, err)
			state := filepath.Join(root, "state")
			locks := filepath.Join(root, "locks")
			logPath := filepath.Join(root, "operations")
			commandPath := filepath.Join(root, "fake-limactl")
			command := `#!/bin/sh
if [ "$1" = "list" ]; then
    printf '{"name":"%s","status":"Running","config":{"mounts":[{"location":"%s","writable":true},{"location":"%s","writable":true}]}}\n' "$FAKE_VM" "$FAKE_PROJECT" "$FAKE_STATE"
    exit 0
fi
if [ "$1" != "shell" ]; then
    exit 99
fi
shift 4
if [ "$1" = "sh" ] && [ "$2" = "-c" ] && [ "$4" = "lja" ]; then
    shift 4
fi
printf '%s\0' "$@" >> "$FAKE_LOG"
case "$1" in
    true)
        exit 0
        ;;
    cat)
        printf 'ID=ubuntu\n'
        exit 0
        ;;
    command)
        case "$3" in
            node|npm)
                if [ -f "$FAKE_ROOT/$3" ]; then
                    printf '/usr/bin/%s\n' "$3"
                    exit 0
                fi
                exit 1
                ;;
            dpkg-query|apt-get|sudo)
                printf '/usr/bin/%s\n' "$3"
                exit 0
                ;;
            codex)
                if [ -f "$FAKE_ROOT/codex" ]; then
                    printf '/usr/bin/codex\n'
                    exit 0
                fi
                exit 1
                ;;
        esac
        exit 1
        ;;
    node|npm)
        if [ -f "$FAKE_ROOT/$1" ]; then
            printf 'v22.0.0\n'
            exit 0
        fi
        exit 42
        ;;
    dpkg-query)
        if [ -f "$FAKE_ROOT/node" ] && [ -f "$FAKE_ROOT/npm" ]; then
            printf 'install ok installed'
        fi
        exit 0
        ;;
    sh)
        if [ "$2" = "-eu" ] && [ "$3" = "-c" ]; then
            : > "$FAKE_ROOT/node"
            : > "$FAKE_ROOT/npm"
            exit 0
        fi
        ;;
    sudo)
        if [ "$2" = "npm" ]; then
            : > "$FAKE_ROOT/codex"
            exit 0
        fi
        ;;
    CODEX_HOME=*)
        shift
        if [ "$1" = "sh" ] && [ "$2" = "-c" ] && [ "$4" = "lja" ]; then
            shift 4
        fi
        if [ "$1" = "codex" ]; then
            exit 17
        fi
        ;;
esac
exit 98
`
			assert.NilError(t, os.WriteFile(commandPath, []byte(command), 0o755))
			if test.nodeInitiallyReady {
				assert.NilError(t, os.WriteFile(filepath.Join(root, "node"), nil, 0o600))
			}
			if test.npmInitiallyReady {
				assert.NilError(t, os.WriteFile(filepath.Join(root, "npm"), nil, 0o600))
			}
			t.Setenv("FAKE_PROJECT", project)
			t.Setenv("FAKE_VM", vmName)
			t.Setenv("FAKE_ROOT", root)
			t.Setenv("FAKE_STATE", state)
			t.Setenv("FAKE_LOG", logPath)
			t.Setenv("HOME", root)

			development := DefaultDevelopmentConfig()
			development.Packages = []string{}
			development.CopyGitConfig = false
			code, err := RunAgent(project, AgentRunOptions{
				AgentName: codexAgentName,
				Workflow: WorkflowOptions{
					StateRoot:     state,
					Development:   &development,
					LimaCommand:   commandPath,
					LockDirectory: locks,
				},
			})
			assert.NilError(t, err)
			assert.Equal(t, code, 17)
			_, err = os.Stat(filepath.Join(root, "node"))
			assert.NilError(t, err)
			_, err = os.Stat(filepath.Join(root, "npm"))
			assert.NilError(t, err)
			_, err = os.Stat(filepath.Join(root, "codex"))
			assert.NilError(t, err)
			configPath := filepath.Join(state, codexStateDirectoryName, codexConfigName)
			_, err = os.Stat(configPath)
			assert.NilError(t, err)
			assert.NilError(t, os.Remove(configPath))
			code, err = RunAgent(project, AgentRunOptions{
				AgentName: codexAgentName,
				Arguments: []string{codeXLoginSubcommand},
				Workflow: WorkflowOptions{
					StateRoot:     state,
					Development:   &development,
					LimaCommand:   commandPath,
					LockDirectory: locks,
				},
			})
			assert.NilError(t, err)
			assert.Equal(t, code, 17)
			_, err = os.Stat(configPath)
			assert.Assert(t, os.IsNotExist(err))

			operations, err := os.ReadFile(logPath)
			assert.NilError(t, err)
			operationText := string(operations)
			assert.Assert(t, !strings.Contains(operationText, "snap"))
			assert.Equal(t, strings.Contains(operationText, "apt-get install"), test.wantNativeInstall)
			assert.Equal(t, strings.Count(operationText, "npm\x00install"), 1)
		})
	}
}

func TestGitConfigCopiesAndWriteScript(t *testing.T) {
	host := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(host, "included"), []byte("[user]\nname = Example\n"), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(host, "excludes"), []byte("secret\n"), 0o600))
	rootConfig := "[include]\npath = included\n[core]\nexcludesFile = excludes\n"
	assert.NilError(t, os.WriteFile(filepath.Join(host, gitConfigName), []byte(rootConfig), 0o600))
	copies, err := GitConfigCopies(host)
	assert.NilError(t, err)
	assert.Assert(t, len(copies) >= 3)
	assert.Assert(t, strings.Contains(string(copies[gitConfigName]), "~/included"))
	script, err := GitConfigWriteScript(".config/lja/git/includes/hash/file")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(script, "cd -- \"$HOME\""))
	_, err = GitConfigWriteScript("../escape")
	assert.Assert(t, err != nil)
}
