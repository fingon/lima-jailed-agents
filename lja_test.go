package lja

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

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
	global := map[string]any{
		"packages":        []string{"git"},
		"env":             map[string]string{"A": "global", "B": "kept"},
		"env_passthrough": []string{"TOKEN", "PROXY"},
		"setup":           []string{"global-command"},
	}
	projectConfig := map[string]any{
		"packages":        []string{"ninja-build", "ninja-build"},
		"env":             map[string]string{"A": "project"},
		"env_passthrough": []string{"TOKEN", "OTHER"},
		"setup":           []string{"project-command"},
		"copy_git_config": false,
	}
	globalBytes, err := json.Marshal(global)
	assert.NilError(t, err)
	projectBytes, err := json.Marshal(projectConfig)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(globalPath, globalBytes, 0o600))
	projectPath := filepath.Join(project, projectConfigName)
	assert.NilError(t, os.WriteFile(projectPath, projectBytes, 0o600))

	config, err := loadDevelopmentConfig(project, map[string]string{xdgConfigHomeEnv: configHome}, host)
	assert.NilError(t, err)
	assert.DeepEqual(t, config.Packages, []string{"ninja-build"})
	assert.Equal(t, config.CopyGitConfig, false)
	assert.DeepEqual(t, config.Env, map[string]string{"A": "project", "B": "kept"})
	assert.DeepEqual(t, config.EnvPassthrough, []string{"TOKEN", "PROXY", "OTHER"})
	assert.Equal(t, len(config.Setup), 2)

	resolved, err := config.ResolveEnvironment(map[string]string{"TOKEN": "literal value", "PROXY": "", "OTHER": "other"})
	assert.NilError(t, err)
	assert.DeepEqual(t, resolved, map[string]string{
		"TOKEN": "literal value", "PROXY": "", "OTHER": "other", "A": "project", "B": "kept",
	})
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
	assert.DeepEqual(t, decoded.Packages, []string{"git", "make"})
	assert.Equal(t, decoded.HasPackages, true)
	assert.Equal(t, decoded.CopyGitConfig, false)
	assert.Equal(t, decoded.HasCopyGitConfig, true)
	assert.DeepEqual(t, decoded.Env, map[string]string{"BUILD_MODE": "development", "BUILD_COUNT": "3"})
	assert.Equal(t, decoded.HasEnv, true)
	assert.DeepEqual(t, decoded.EnvPassthrough, []string{"TOKEN"})
	assert.Equal(t, decoded.HasEnvPassthrough, true)
	assert.DeepEqual(t, decoded.Setup, []string{"make dep\nmake build\n"})
	assert.Equal(t, decoded.HasSetup, true)
	assert.Equal(t, decoded.InheritSetup, false)
	assert.Equal(t, decoded.HasInheritSetup, true)
	assert.Equal(t, decoded.InheritEnvironmentPassthrough, true)
	assert.Equal(t, decoded.HasInheritEnvironmentPassthrough, true)

	decoded, err = decodeDevelopmentConfig([]byte("{}\n"), true)
	assert.NilError(t, err)
	assert.Equal(t, decoded.HasPackages, false)
	assert.Equal(t, decoded.HasCopyGitConfig, false)
	assert.Equal(t, decoded.HasEnv, false)
	assert.Equal(t, decoded.HasEnvPassthrough, false)
	assert.Equal(t, decoded.HasSetup, false)
}

func TestDevelopmentConfigurationRejectsInvalidYAML(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "unknown setting", content: []byte("unknown: true\n"), want: "unknown settings: unknown"},
		{name: "duplicate setting", content: []byte("packages: []\npackages: []\n"), want: "duplicate configuration key: packages"},
		{name: "duplicate environment", content: []byte("env:\n  BUILD_MODE: one\n  BUILD_MODE: two\n"), want: "duplicate env key: BUILD_MODE"},
		{name: "null setting", content: []byte("packages: null\n"), want: "packages must not be null"},
		{name: "null environment value", content: []byte("env:\n  BUILD_MODE: null\n"), want: "env must be a mapping with string values"},
		{name: "scalar coercion", content: []byte("copy_git_config: \"true\"\n"), want: "copy_git_config must be a boolean"},
		{name: "array scalar coercion", content: []byte("packages: [git, true]\n"), want: "packages must be a string"},
		{name: "root sequence", content: []byte("- git\n"), want: "configuration must be a mapping"},
		{name: "multiple documents", content: []byte("{}\n---\n{}\n"), want: "single YAML document"},
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
	configHome := filepath.Join(root, "config")
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
	configHome := filepath.Join(root, "config")
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

func TestDevelopmentConfigurationYAMLOutputIsDeterministic(t *testing.T) {
	config := DevelopmentConfig{
		Packages:      []string{"make", "git"},
		CopyGitConfig: false,
		Env: map[string]string{
			"Z_LAST":  "last",
			"A_FIRST": "first",
		},
		EnvPassthrough: []string{"TOKEN"},
		Setup:          []SetupCommand{{Source: "/private/source.yaml", Index: 7, Command: "make dep\nmake build\n"}},
	}
	want := "packages:\n  - make\n  - git\ncopy_git_config: false\nenv:\n  A_FIRST: first\n  Z_LAST: last\nenv_passthrough:\n  - TOKEN\nsetup:\n  - |\n    make dep\n    make build\n"
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
	assert.Assert(t, strings.Contains(string(empty), "setup: []\n"))
	assert.Assert(t, strings.HasSuffix(string(empty), "\n"))
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

	unsupported := `projects = {"/one" = {trust_level = "trusted"}}` + "\n"
	_, err = CodexTrustedConfig(unsupported, []string{project})
	assert.ErrorContains(t, err, "unsupported")
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
	assert.NilError(t, ensureCodexDirectoryTrust(state, []string{project}, filepath.Join(root, "locks")))
	after, err := os.ReadFile(configPath)
	assert.NilError(t, err)
	assert.DeepEqual(t, after, before)
}

func TestAgentInvocationAndWrappers(t *testing.T) {
	arguments, environment, err := BuildAgentInvocation("codex", []string{"exec", "prompt with spaces"})
	assert.NilError(t, err)
	assert.DeepEqual(t, arguments, []string{"exec", codeXPermissionFlag, "prompt with spaces"})
	assert.Equal(t, len(environment), 0)
	arguments, _, err = BuildAgentInvocation("claude", []string{"auth", "login"})
	assert.NilError(t, err)
	assert.DeepEqual(t, arguments, []string{"auth", "login"})
	arguments, environment, err = BuildAgentInvocation("opencode", []string{"run", "prompt"})
	assert.NilError(t, err)
	assert.DeepEqual(t, arguments, []string{"run", "prompt"})
	assert.Equal(t, environment[openCodeConfigContentEnvironment], openCodePermissionConfig)

	state := filepath.Join(t.TempDir(), "state")
	content, err := AgentWrapperContent(state, "codex", "/usr/bin/codex")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(content, "unset "+codeXHomeEnvironment))
	assert.Assert(t, strings.Contains(content, codeXPermissionFlag))
	assert.Assert(t, strings.Contains(content, "login"))
}

func TestGuestProcessPreservesArgumentAndEnvironmentBoundaries(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	logPath := filepath.Join(root, "arguments.log")
	commandPath := filepath.Join(root, "fake-limactl")
	command := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(logPath) + "\nexit 23\n"
	assert.NilError(t, os.WriteFile(commandPath, []byte(command), 0o755))

	options := defaultProcessOptions(commandPath)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, "lja-test", []string{"printf", "$(touch nope);", "word with spaces"}, options, map[string]string{
		"Z_VALUE": "value with spaces",
		"A_VALUE": "literal;value",
	})
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 23)
	encoded, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	actual := strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n")
	assert.DeepEqual(t, actual, []string{
		"shell",
		"--workdir",
		project,
		"lja-test",
		"A_VALUE=literal;value",
		"Z_VALUE=value with spaces",
		"printf",
		"$(touch nope);",
		"word with spaces",
	})

	_, err = runGuest(project, "lja-test", []string{"true"}, options, map[string]string{"BAD": "contains\x00nul"})
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
		"CODEX_HOME": filepath.Join(host, "codex"),
	})
	assert.NilError(t, err)
	assert.Equal(t, changed, true)
	_, destination, err := AgentInstructionPaths(state, "codex", map[string]string{
		"CODEX_HOME": filepath.Join(host, "codex"),
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
		"CODEX_HOME": filepath.Join(host, "codex"),
	})
	assert.NilError(t, err)
	assert.Equal(t, changed, true)
	content, err = os.ReadFile(destination)
	assert.NilError(t, err)
	assert.Equal(t, string(content), "second\n")

	assert.NilError(t, os.Remove(filepath.Join(host, "codex", "AGENTS.md")))
	changed, err = RefreshAgentInstructions(state, "codex", map[string]string{
		"CODEX_HOME": filepath.Join(host, "codex"),
	})
	assert.NilError(t, err)
	assert.Equal(t, changed, false)
	content, err = os.ReadFile(destination)
	assert.NilError(t, err)
	assert.Equal(t, string(content), "second\n")
}

func TestAgentLaunchUsesValidatedVMAndRetryableGuestInstallation(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	vmName, err := ProjectVMName(project)
	assert.NilError(t, err)
	state := filepath.Join(root, "state")
	locks := filepath.Join(root, "locks")
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
case "$1" in
    true)
        exit 0
        ;;
    command)
        case "$3" in
            node|npm)
                if [ -f "$FAKE_ROOT/node" ]; then
                    printf '/usr/bin/%s\n' "$3"
                    exit 0
                fi
                exit 1
                ;;
            snap)
                printf '/usr/bin/snap\n'
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
    dpkg-query)
        printf 'install ok installed'
        exit 0
        ;;
    sudo)
        if [ "$2" = "snap" ]; then
            : > "$FAKE_ROOT/node"
            exit 0
        fi
        if [ "$2" = "npm" ]; then
            : > "$FAKE_ROOT/codex"
            exit 0
        fi
        ;;
    CODEX_HOME=*)
        shift
        if [ "$1" = "codex" ]; then
            exit 17
        fi
        ;;
esac
exit 98
`
	assert.NilError(t, os.WriteFile(commandPath, []byte(command), 0o755))
	t.Setenv("FAKE_PROJECT", project)
	t.Setenv("FAKE_VM", vmName)
	t.Setenv("FAKE_ROOT", root)
	t.Setenv("FAKE_STATE", state)
	t.Setenv("HOME", root)

	development := DefaultDevelopmentConfig()
	development.Packages = []string{}
	development.CopyGitConfig = false
	code, err := RunAgent(project, codexAgentName, nil, nil, "", WorkflowOptions{
		StateRoot:     state,
		Development:   &development,
		LimaCommand:   commandPath,
		LockDirectory: locks,
	})
	assert.NilError(t, err)
	assert.Equal(t, code, 17)
	_, err = os.Stat(filepath.Join(root, "node"))
	assert.NilError(t, err)
	_, err = os.Stat(filepath.Join(root, "codex"))
	assert.NilError(t, err)
	_, err = os.Stat(filepath.Join(state, codexStateDirectoryName, codexConfigName))
	assert.NilError(t, err)
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
