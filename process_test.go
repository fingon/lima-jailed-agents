package lja

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func runGuestPathBootstrap(t *testing.T, arguments []string, environment []string) []string {
	t.Helper()
	commandArguments := []string{shellCommandFlag, guestPathBootstrapScript, programName}
	commandArguments = append(commandArguments, arguments...)
	command := exec.Command("/bin/sh", commandArguments...)
	command.Env = environment
	output, err := command.Output()
	assert.NilError(t, err)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(output)), ":")
}

func TestGuestPathBootstrap(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	emptyPath := filepath.Join(root, "empty")
	assert.NilError(t, os.MkdirAll(home, 0o755))
	assert.NilError(t, os.Mkdir(emptyPath, 0o755))

	tests := []struct {
		name        string
		environment []string
		want        []string
	}{
		{
			name:        "defaults",
			environment: []string{"HOME=" + home, "PATH=" + emptyPath},
			want:        []string{filepath.Join(home, "go", "bin"), filepath.Join(home, ".local", "bin"), emptyPath},
		},
		{
			name: "configured paths",
			environment: []string{
				"HOME=" + home,
				"PATH=" + emptyPath,
				"PIPX_BIN_DIR=" + filepath.Join(root, "pipx-bin"),
				"GOBIN=" + filepath.Join(root, "go-bin"),
				"GOPATH=" + filepath.Join(root, "gopath") + ":" + filepath.Join(root, "second-gopath"),
			},
			want: []string{
				filepath.Join(root, "gopath", "bin"),
				filepath.Join(root, "go-bin"),
				filepath.Join(root, "pipx-bin"),
				filepath.Join(home, "go", "bin"),
				filepath.Join(home, ".local", "bin"),
				emptyPath,
			},
		},
		{
			name: "configured paths are deduplicated",
			environment: []string{
				"HOME=" + home,
				"PATH=" + emptyPath,
				"PIPX_BIN_DIR=" + filepath.Join(home, ".local", "bin"),
				"GOBIN=" + filepath.Join(home, "go", "bin"),
				"GOPATH=" + filepath.Join(home, "go"),
			},
			want: []string{filepath.Join(home, "go", "bin"), filepath.Join(home, ".local", "bin"), emptyPath},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := runGuestPathBootstrap(t, []string{"/bin/sh", shellCommandFlag, `printf '%s\n' "$PATH"`}, test.environment)
			assert.DeepEqual(t, actual, test.want)
		})
	}

	fakeGoDirectory := filepath.Join(root, "fake-go")
	assert.NilError(t, os.Mkdir(fakeGoDirectory, 0o755))
	fakeGoPath := filepath.Join(root, "effective-gopath")
	fakeGoBin := filepath.Join(root, "effective-gobin")
	fakeGo := filepath.Join(fakeGoDirectory, "go")
	fakeGoScript := "#!/bin/sh\ncase \"$1:$2\" in\nenv:GOBIN) printf '%s\\n' \"${FAKE_GOBIN-}\" ;;\nenv:GOPATH) printf '%s\\n' \"${FAKE_GOPATH-}\" ;;\n*) exit 2 ;;\nesac\n"
	assert.NilError(t, os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755))

	for _, test := range []struct {
		name   string
		goBin  string
		goPath string
		want   string
	}{
		{name: "effective GOBIN", goBin: fakeGoBin, goPath: fakeGoPath, want: fakeGoBin},
		{name: "effective GOPATH", goPath: fakeGoPath, want: filepath.Join(fakeGoPath, "bin")},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := []string{
				"HOME=" + home,
				"PATH=" + fakeGoDirectory + ":" + emptyPath,
				"FAKE_GOBIN=" + test.goBin,
				"FAKE_GOPATH=" + test.goPath,
			}
			actual := runGuestPathBootstrap(t, []string{"/bin/sh", shellCommandFlag, `printf '%s\n' "$PATH"`}, environment)
			assert.Equal(t, actual[0], test.want)
		})
	}
}

func TestGuestPathBootstrapRunsCommandProbe(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, codexAgentName)
	assert.NilError(t, os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755))

	commandArguments := []string{shellCommandFlag, guestPathBootstrapScript, programName, guestCommandProbe, guestCommandProbeFlag, codexAgentName}
	command := exec.Command("/bin/sh", commandArguments...)
	command.Env = []string{"PATH=" + root}
	output, err := command.Output()
	assert.NilError(t, err)
	if err != nil {
		return
	}
	assert.Equal(t, string(output), executable+"\n")
}
