package lja

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
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
		{name: "empty", exitCode: failureExitCode, wantError: failureMessage},
		{name: "whitespace", stderr: " \t\n", exitCode: failureExitCode, wantError: failureMessage},
		{name: "success", stderr: diagnostic, exitCode: "0"},
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
		{name: "no existing entries", environment: map[string]string{"BUILD_MODE": "test"}},
		{name: "missing count", environment: map[string]string{gitConfigKeyEnvPrefix + "0": "user.name"}, wantError: "GIT_CONFIG_COUNT"},
		{name: "invalid count", environment: map[string]string{gitConfigCountEnv: "nope"}, wantError: "GIT_CONFIG_COUNT"},
		{name: "missing value", environment: map[string]string{gitConfigCountEnv: "1", gitConfigKeyEnvPrefix + "0": "user.name"}, wantError: "entry 0"},
		{name: "outside count", environment: map[string]string{gitConfigCountEnv: "0", gitConfigKeyEnvPrefix + "0": "user.name", gitConfigValueEnvPrefix + "0": "Example"}, wantError: "outside"},
		{name: "invalid key", environment: map[string]string{gitConfigCountEnv: "1", gitConfigKeyEnvPrefix + "0": "", gitConfigValueEnvPrefix + "0": "Example"}, wantError: "invalid"},
		{name: "NUL value", environment: map[string]string{gitConfigCountEnv: "1", gitConfigKeyEnvPrefix + "0": "user.name", gitConfigValueEnvPrefix + "0": "bad\x00value"}, wantError: "NUL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := githubGitEnvironment(test.environment)
			if test.wantError != "" {
				assert.ErrorContains(t, err, test.wantError)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, result["BUILD_MODE"], "test")
			assert.Equal(t, result[gitConfigCountEnv], "4")
			assert.Equal(t, result[gitConfigKeyEnvPrefix+"0"], githubCredentialKey)
			assert.Equal(t, result[gitConfigValueEnvPrefix+"0"], "")
			assert.Equal(t, result[gitConfigValueEnvPrefix+"1"], githubGitHelper)
			assert.Equal(t, result[gitConfigValueEnvPrefix+"2"], githubURLInputPrefixes[0])
			assert.Equal(t, result[gitConfigValueEnvPrefix+"3"], githubURLInputPrefixes[1])
		})
	}

	existing := map[string]string{
		"BUILD_MODE":                  "test",
		gitConfigCountEnv:             "1",
		gitConfigKeyEnvPrefix + "0":   "user.name",
		gitConfigValueEnvPrefix + "0": "Example",
	}
	result, err := githubGitEnvironment(existing)
	assert.NilError(t, err)
	assert.Equal(t, result[gitConfigCountEnv], "5")
	assert.Equal(t, result[gitConfigKeyEnvPrefix+"0"], "user.name")
	assert.Equal(t, result[gitConfigValueEnvPrefix+"0"], "Example")
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
			wantError: "conflicting GitHub URL rewrite",
		},
		{
			name:      "GitHub push rewrite",
			content:   "[url \"ssh://git@github.com/\"]\n\tpushInsteadOf = ssh://git@github.com/\n",
			wantError: "conflicting GitHub URL rewrite",
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
