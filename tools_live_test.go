package lja

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

const (
	realLimaTestEnv      = "LJA_TEST_REAL_LIMA"
	realLimaTemplatesEnv = "LJA_TEST_REAL_LIMA_TEMPLATES"
)

func TestLogicalToolsLiveGuestSmoke(t *testing.T) {
	if os.Getenv(realLimaTestEnv) != "1" {
		t.Skip("set LJA_TEST_REAL_LIMA=1 to exercise disposable Lima guests")
	}
	limactlPath, err := exec.LookPath(limaCtlCommand)
	if err != nil {
		t.Skipf("Lima is not installed: %v", err)
	}
	templates := os.Getenv(realLimaTemplatesEnv)
	if templates == "" {
		templates = "template:default,template:fedora"
	}
	for template := range strings.SplitSeq(templates, ",") {
		template = strings.TrimSpace(template)
		if template == "" {
			continue
		}
		t.Run(template, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "project")
			assert.NilError(t, os.Mkdir(project, 0o755))
			config := DevelopmentConfig{
				Lima:          map[string]any{limaBaseKey: template},
				Packages:      []string{},
				Tools:         []string{logicalToolPrek},
				CopyGitConfig: false,
			}
			options := WorkflowOptions{
				Development:   &config,
				LimaCommand:   limactlPath,
				LockDirectory: filepath.Join(root, "locks"),
			}
			vmName, err := ProjectVMName(project)
			assert.NilError(t, err)
			t.Cleanup(func() {
				assert.NilError(t, DeleteVM(project, limactlPath))
			})
			_, err = CreateVM(project, options)
			assert.NilError(t, err)
			for _, executable := range []string{pipxCommand, logicalToolUV, logicalToolPrek} {
				result, runErr := RunGuest(project, vmName, []string{executable, versionFlag}, GuestOptions{Command: limactlPath})
				assert.NilError(t, runErr)
				assert.Equal(t, result.ExitCode, 0)
			}
		})
	}
}
