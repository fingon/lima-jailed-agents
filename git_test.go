package lja

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

const (
	successTestName               = "success"
	githubURLRewriteConflictError = "conflicting GitHub URL rewrite"
	githubExampleHTTPSRemote      = "https://github.com/owner/repo"
)

func TestHostGitConfigDiagnostics(t *testing.T) {
	const (
		configPath          = "/example/.gitconfig"
		failureExitCode     = "69"
		failureMessage      = "cannot process Git config " + configPath + ": Git exited " + failureExitCode
		multilineDiagnostic = "first line\nsecond line"
		stdout              = "configuration output\n"
		diagnostic          = "Review and accept the Xcode license."
	)
	for _, test := range []struct {
		name      string
		stderr    string
		exitCode  string
		wantError string
	}{
		{name: "diagnostic", stderr: "  " + diagnostic + "\n", exitCode: failureExitCode, wantError: failureMessage + ": " + diagnostic},
		{name: "multiline", stderr: multilineDiagnostic + "\n", exitCode: failureExitCode, wantError: failureMessage + ": " + multilineDiagnostic},
		{name: emptyTestName, exitCode: failureExitCode, wantError: failureMessage},
		{name: "whitespace", stderr: " \t\n", exitCode: failureExitCode, wantError: failureMessage},
		{name: successTestName, stderr: diagnostic, exitCode: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := t.TempDir()
			script := "#!/bin/sh\nprintf '%s' \"$LJA_TEST_GIT_STDOUT\"\nprintf '%s' \"$LJA_TEST_GIT_STDERR\" >&2\nexit \"$LJA_TEST_GIT_EXIT_CODE\"\n"
			assert.NilError(t, os.WriteFile(filepath.Join(bin, gitCommand), []byte(script), 0o700))
			t.Setenv("PATH", bin)
			t.Setenv("LJA_TEST_GIT_STDOUT", stdout)
			t.Setenv("LJA_TEST_GIT_STDERR", test.stderr)
			t.Setenv("LJA_TEST_GIT_EXIT_CODE", test.exitCode)

			output, err := hostGitConfig(configPath, "--null", "--list")
			if test.wantError != "" {
				assert.Error(t, err, test.wantError)
				assert.Assert(t, output == nil)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, string(output), stdout)
		})
	}
}

func TestGitHubGitEnvironment(t *testing.T) {
	for _, test := range []struct {
		name        string
		environment map[string]string
		wantError   string
	}{
		{name: "no existing entries", environment: map[string]string{buildModeEnvironment: testEnvironmentValue}},
		{name: "missing count", environment: map[string]string{gitConfigKeyEnvPrefix + "0": gitUserNameKey}, wantError: gitConfigCountEnv},
		{name: "invalid count", environment: map[string]string{gitConfigCountEnv: "nope"}, wantError: "GIT_CONFIG_COUNT"},
		{name: "missing value", environment: map[string]string{gitConfigCountEnv: "1", gitConfigKeyEnvPrefix + "0": gitUserNameKey}, wantError: "entry 0"},
		{name: "outside count", environment: map[string]string{gitConfigCountEnv: "0", gitConfigKeyEnvPrefix + "0": gitUserNameKey, gitConfigValueEnvPrefix + "0": exampleUserName}, wantError: "outside"},
		{name: "invalid key", environment: map[string]string{gitConfigCountEnv: "1", gitConfigKeyEnvPrefix + "0": "", gitConfigValueEnvPrefix + "0": exampleUserName}, wantError: invalidTestError},
		{name: "NUL value", environment: map[string]string{gitConfigCountEnv: "1", gitConfigKeyEnvPrefix + "0": gitUserNameKey, gitConfigValueEnvPrefix + "0": "bad\x00value"}, wantError: "NUL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := githubGitEnvironment(test.environment)
			if test.wantError != "" {
				assert.ErrorContains(t, err, test.wantError)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, result[buildModeEnvironment], testEnvironmentValue)
			assert.Equal(t, result[gitConfigCountEnv], "4")
			assert.Equal(t, result[gitConfigKeyEnvPrefix+"0"], githubCredentialKey)
			assert.Equal(t, result[gitConfigValueEnvPrefix+"0"], "")
			assert.Equal(t, result[gitConfigValueEnvPrefix+"1"], githubGitHelper)
			assert.Equal(t, result[gitConfigValueEnvPrefix+"2"], githubURLInputPrefixes[0])
			assert.Equal(t, result[gitConfigValueEnvPrefix+"3"], githubURLInputPrefixes[1])
		})
	}

	existing := map[string]string{
		buildModeEnvironment:          testEnvironmentValue,
		gitConfigCountEnv:             "1",
		gitConfigKeyEnvPrefix + "0":   gitUserNameKey,
		gitConfigValueEnvPrefix + "0": exampleUserName,
	}
	result, err := githubGitEnvironment(existing)
	assert.NilError(t, err)
	assert.Equal(t, result[gitConfigCountEnv], "5")
	assert.Equal(t, result[gitConfigKeyEnvPrefix+"0"], gitUserNameKey)
	assert.Equal(t, result[gitConfigValueEnvPrefix+"0"], exampleUserName)
	assert.Equal(t, existing[gitConfigCountEnv], "1")
}

func TestGitHubGitConfigRewriteConflicts(t *testing.T) {
	for _, test := range []struct {
		name      string
		content   string
		wantError string
	}{
		{
			name:    "managed HTTPS rewrite",
			content: "[url \"https://github.com/\"]\n\tinsteadOf = git@github.com:\n",
		},
		{
			name:    "other host",
			content: "[url \"ssh://git@gitlab.com/\"]\n\tinsteadOf = git@gitlab.com:\n",
		},
		{
			name:      "GitHub SSH rewrite",
			content:   "[url \"ssh://git@github.com/\"]\n\tinsteadOf = git@github.com:\n",
			wantError: githubURLRewriteConflictError,
		},
		{
			name:      "GitHub push rewrite",
			content:   "[url \"ssh://git@github.com/\"]\n\tpushInsteadOf = ssh://git@github.com/\n",
			wantError: githubURLRewriteConflictError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := githubGitURLRewriteConflicts(map[string][]byte{gitConfigName: []byte(test.content)})
			if test.wantError == "" {
				assert.NilError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.wantError)
			assert.Assert(t, !strings.Contains(err.Error(), "token"))
		})
	}
}

func runGitWithGitHubEnvironment(t *testing.T, arguments ...string) string {
	t.Helper()
	environment, err := githubGitEnvironment(nil)
	assert.NilError(t, err)
	command := exec.Command(gitCommand, arguments...)
	commandEnvironment := make([]string, 0, len(os.Environ())+len(environment))
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, gitConfigCountEnv+"=") && !strings.HasPrefix(value, gitConfigKeyEnvPrefix) && !strings.HasPrefix(value, gitConfigValueEnvPrefix) {
			commandEnvironment = append(commandEnvironment, value)
		}
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		commandEnvironment = append(commandEnvironment, name+"="+environment[name])
	}
	command.Env = commandEnvironment
	output, err := command.Output()
	assert.NilError(t, err)
	return strings.TrimSpace(string(output))
}

func TestGitHubGitRewritesFetchPushAndLeavesOtherHosts(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command(gitCommand, "-C", repository, "init", "--quiet")
	assert.NilError(t, command.Run())
	assert.NilError(t, exec.Command(gitCommand, "-C", repository, "remote", "add", "origin", "git@github.com:owner/repo").Run())
	for _, test := range []struct {
		name      string
		remote    string
		rewritten string
	}{
		{name: "scp SSH", remote: "git@github.com:owner/repo", rewritten: githubExampleHTTPSRemote},
		{name: "URL SSH", remote: "ssh://git@github.com/owner/repo", rewritten: githubExampleHTTPSRemote},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.NilError(t, exec.Command(gitCommand, "-C", repository, "remote", "set-url", "origin", test.remote).Run())
			assert.Equal(t, runGitWithGitHubEnvironment(t, "-C", repository, "remote", "get-url", "origin"), test.rewritten)
			assert.Equal(t, runGitWithGitHubEnvironment(t, "-C", repository, "remote", "get-url", "--push", "origin"), test.rewritten)
			remoteContent, err := os.ReadFile(filepath.Join(repository, ".git", "config"))
			assert.NilError(t, err)
			assert.Assert(t, strings.Contains(string(remoteContent), test.remote))
		})
	}
	assert.NilError(t, exec.Command(gitCommand, "-C", repository, "remote", "remove", "origin").Run())
	assert.NilError(t, exec.Command(gitCommand, "-C", repository, "remote", "add", "origin", "git@gitlab.com:owner/repo").Run())
	assert.Equal(t, runGitWithGitHubEnvironment(t, "-C", repository, "remote", "get-url", "origin"), "git@gitlab.com:owner/repo")
	assert.Equal(t, runGitWithGitHubEnvironment(t, "-C", repository, "remote", "get-url", "--push", "origin"), "git@gitlab.com:owner/repo")
}
