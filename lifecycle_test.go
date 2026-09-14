package lja

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

const (
	lifecycleListEnv          = "LJA_TEST_LIST"
	lifecycleLogEnv           = "LJA_TEST_LOG"
	lifecycleListFailEnv      = "LJA_TEST_LIST_FAIL"
	lifecycleOperationFailEnv = "LJA_TEST_OPERATION_FAIL"
	lifecycleOtherVM          = "unrelated"
)

func TestLifecycleParsing(t *testing.T) {
	for _, command := range []string{"stop", "delete"} {
		for _, flag := range []string{"", "-a", "--all"} {
			t.Run(command+flag, func(t *testing.T) {
				cli := CLI{}
				parser, err := newParser(&cli)
				assert.NilError(t, err)
				arguments := []string{command}
				if flag != "" {
					arguments = append(arguments, flag)
				}
				context, err := parser.Parse(arguments)
				assert.NilError(t, err)
				assert.Equal(t, commandName(context), command)
				assert.Equal(t, cli.allVMs(command), flag != "")
			})
		}
	}
	parser, err := newParser(&CLI{})
	assert.NilError(t, err)
	_, err = parser.Parse([]string{"stop-all"})
	assert.Assert(t, err != nil)
}

func lifecycleFixture(t *testing.T, instances []LimaInstance) (string, string) {
	t.Helper()
	root := t.TempDir()
	command := filepath.Join(root, limaCtlCommand)
	script, err := os.ReadFile("testdata/lifecycle/limactl.sh")
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(command, script, 0o755))
	list := filepath.Join(root, "list.json")
	entries := make([]map[string]any, 0, len(instances))
	for _, instance := range instances {
		entries = append(entries, map[string]any{
			"name": instance.Name, "status": instance.Status,
			"config": map[string]any{"mounts": []any{}},
		})
	}
	payload, err := json.Marshal(entries)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(list, payload, 0o600))
	log := filepath.Join(root, "operations.log")
	assert.NilError(t, os.WriteFile(log, nil, 0o600))
	t.Setenv(lifecycleListEnv, list)
	t.Setenv(lifecycleLogEnv, log)
	t.Setenv(lifecycleListFailEnv, "0")
	t.Setenv(lifecycleOperationFailEnv, "0")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return command, log
}

func assertLifecycleLog(t *testing.T, path string, expected string) {
	t.Helper()
	actual, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(actual), expected)
}

func TestDeleteProject(t *testing.T) {
	for _, status := range []string{"", limaStatusRunning, limaStatusStopped, limaStatusBroken, limaStatusInstalling, limaStatusUninitialized} {
		t.Run(status, func(t *testing.T) {
			project := t.TempDir()
			name, err := projectVMName(project)
			assert.NilError(t, err)
			instances := []LimaInstance{{Name: lifecycleOtherVM, Status: limaStatusRunning}}
			if status != "" {
				instances = append(instances, LimaInstance{Name: name, Status: status})
			}
			command, log := lifecycleFixture(t, instances)
			assert.NilError(t, DeleteVM(project, command))
			expected := ""
			if status != "" {
				expected = "delete\n-f\n" + name + "\n"
			}
			assertLifecycleLog(t, log, expected)
		})
	}
}

func TestBulkLifecycle(t *testing.T) {
	for _, command := range []string{"stop", "delete"} {
		for _, flag := range []string{"-a", "--all"} {
			t.Run(command+flag, func(t *testing.T) {
				running := vmNamePrefix + "running"
				stopped := vmNamePrefix + "stopped"
				_, log := lifecycleFixture(t, []LimaInstance{
					{Name: running, Status: limaStatusRunning},
					{Name: lifecycleOtherVM, Status: limaStatusRunning},
					{Name: stopped, Status: limaStatusStopped},
				})
				assert.Equal(t, Main([]string{"--project", "/nonexistent/lja-project", command, flag}), 0)
				expected := "stop\n" + running + "\n"
				if command == "delete" {
					expected = "delete\n-f\n" + running + "\ndelete\n-f\n" + stopped + "\n"
				}
				assertLifecycleLog(t, log, expected)
			})
		}
	}
}

func TestDeleteAllStatesAndFailures(t *testing.T) {
	for _, scenario := range []string{"all-states", "empty", "list-failure", "delete-failure", "malformed", "unknown-status"} {
		t.Run(scenario, func(t *testing.T) {
			instances := []LimaInstance{}
			expected := ""
			if scenario != "empty" {
				for _, status := range []string{limaStatusRunning, limaStatusStopped, limaStatusBroken, limaStatusInstalling, limaStatusUninitialized} {
					name := vmNamePrefix + status
					instances = append(instances, LimaInstance{Name: name, Status: status})
					expected += "delete\n-f\n" + name + "\n"
				}
			}
			if scenario == "unknown-status" {
				instances[0].Status = "Unknown"
			}
			command, log := lifecycleFixture(t, instances)
			switch scenario {
			case "list-failure":
				t.Setenv(lifecycleListFailEnv, "1")
			case "delete-failure":
				t.Setenv(lifecycleOperationFailEnv, "1")
			case "malformed":
				assert.NilError(t, os.WriteFile(os.Getenv(lifecycleListEnv), []byte("invalid"), 0o600))
			}
			err := DeleteAllVMs(command)
			switch scenario {
			case "all-states", "empty":
				assert.NilError(t, err)
			case "delete-failure":
				assert.ErrorContains(t, err, "exit status 23")
				expected = "delete\n-f\n" + instances[0].Name + "\n"
			default:
				assert.Assert(t, err != nil)
				expected = ""
			}
			assertLifecycleLog(t, log, expected)
		})
	}
}

func TestDeleteIgnoresConfigurationAndPreservesHostFiles(t *testing.T) {
	project := t.TempDir()
	state := t.TempDir()
	config := filepath.Join(project, projectConfigName)
	stateFile := filepath.Join(state, "state")
	content := []byte("invalid: [")
	assert.NilError(t, os.WriteFile(config, content, 0o600))
	assert.NilError(t, os.WriteFile(stateFile, content, 0o600))
	name, err := projectVMName(project)
	assert.NilError(t, err)
	_, log := lifecycleFixture(t, []LimaInstance{{Name: name, Status: limaStatusRunning}})
	assert.Equal(t, Main([]string{"--project", project, "--state-dir", state, "delete"}), 0)
	assertLifecycleLog(t, log, "delete\n-f\n"+name+"\n")
	for _, path := range []string{config, stateFile} {
		actual, err := os.ReadFile(path)
		assert.NilError(t, err)
		assert.DeepEqual(t, actual, content)
	}
}

func TestStopProjectLifecycle(t *testing.T) {
	for _, status := range []string{"", limaStatusStopped, limaStatusRunning} {
		t.Run(status, func(t *testing.T) {
			project, err := canonicalProjectPath(t.TempDir())
			assert.NilError(t, err)
			name, err := projectVMName(project)
			assert.NilError(t, err)
			command, log := lifecycleFixture(t, nil)
			if status != "" {
				payload, err := json.Marshal([]map[string]any{{
					"name": name, "status": status,
					"config": map[string]any{"mounts": []map[string]any{{
						"location": project, "mountPoint": project, "writable": true,
					}}},
				}})
				assert.NilError(t, err)
				assert.NilError(t, os.WriteFile(os.Getenv(lifecycleListEnv), payload, 0o600))
			}
			assert.NilError(t, StopVM(project, project, command, t.TempDir()))
			expected := ""
			if status == limaStatusRunning {
				expected = "stop\n" + name + "\n"
			}
			assertLifecycleLog(t, log, expected)
		})
	}
}
