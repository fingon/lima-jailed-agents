package lja

import (
	"os"
	"path/filepath"
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
