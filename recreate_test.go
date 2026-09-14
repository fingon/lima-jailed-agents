package lja

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

const (
	vmProcessEnv      = "LJA_TEST_VM_PROCESS"
	vmDatabaseEnv     = "LJA_TEST_VM_DATABASE"
	vmFailureEnv      = "LJA_TEST_VM_FAILURE"
	vmBinaryEnv       = "LJA_TEST_BINARY"
	testOriginalID    = "original"
	testReplacementID = "replacement"
	testSetupCommand  = "test-setup"
)

type testMount struct {
	Location   string `json:"location"`
	MountPoint string `json:"mountPoint"`
	Writable   bool   `json:"writable"`
}

type testVM struct {
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Config     map[string]any `json:"config"`
	ID         string
	SetupCount int
}

type vmDatabase struct {
	VMs        map[string]testVM
	Operations [][]string
	Failed     int
}

func (database *vmDatabase) save() error {
	payload, err := json.Marshal(database)
	if err != nil {
		return err
	}
	return os.WriteFile(os.Getenv(vmDatabaseEnv), payload, 0o600)
}

func readVMDatabase() (vmDatabase, error) {
	var database vmDatabase
	payload, err := os.ReadFile(os.Getenv(vmDatabaseEnv))
	if err != nil {
		return database, err
	}
	err = json.Unmarshal(payload, &database)
	return database, err
}

func TestVMProcess(t *testing.T) {
	if os.Getenv(vmProcessEnv) != "1" {
		return
	}
	if err := runVMProcess(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(23)
	}
	os.Exit(0)
}

func runVMProcess() error {
	database, err := readVMDatabase()
	if err != nil {
		return err
	}
	var arguments []string
	for index, argument := range os.Args {
		if argument == "--" {
			arguments = os.Args[index+1:]
			break
		}
	}
	if len(arguments) == 0 {
		return fmt.Errorf("missing fake Lima operation")
	}
	operation := arguments[0]
	if operation == "list" {
		instances := make([]testVM, 0, len(database.VMs))
		for _, instance := range database.VMs {
			instances = append(instances, instance)
		}
		return json.NewEncoder(os.Stdout).Encode(instances)
	}
	database.Operations = append(database.Operations, arguments)
	failures := strings.Split(os.Getenv(vmFailureEnv), "|")
	failure := ""
	if database.Failed < len(failures) {
		failure = failures[database.Failed]
	}
	name := arguments[len(arguments)-1]
	stage := operation
	if operation == "rename" {
		if arguments[1] == "--help" {
			stage = "rename-help"
		} else if strings.HasPrefix(name, backupNamePrefix) {
			stage = "rename-old"
		} else {
			stage = "rename-new"
		}
	}
	if operation == "start" || operation == "stop" {
		if strings.HasPrefix(name, replacementNamePrefix) {
			stage += "-candidate"
		} else {
			stage += "-final"
		}
	}
	if operation == "delete" {
		if strings.HasPrefix(name, backupNamePrefix) {
			stage = "delete-backup"
		}
	}
	if operation == "shell" {
		if arguments[len(arguments)-1] == testSetupCommand {
			stage = "setup"
		} else {
			stage = "connect"
		}
	}
	if failure == stage || failure == stage+"-partial" {
		database.Failed++
		if strings.HasSuffix(failure, "-partial") {
			from := arguments[len(arguments)-2]
			partial := database.VMs[from]
			partial.Name = name
			database.VMs[name] = partial
		}
		if err := database.save(); err != nil {
			return err
		}
		return fmt.Errorf("injected failure: %s", failure)
	}
	switch operation {
	case "rename":
		if arguments[1] != "--help" {
			from := arguments[len(arguments)-2]
			instance, present := database.VMs[from]
			if !present || instance.Status == limaStatusRunning {
				return fmt.Errorf("cannot rename %s", from)
			}
			if _, exists := database.VMs[name]; exists {
				return fmt.Errorf("rename destination exists")
			}
			delete(database.VMs, from)
			instance.Name = name
			database.VMs[name] = instance
		}
	case "create":
		instance := testVM{Status: limaStatusStopped, ID: testReplacementID, Config: map[string]any{}}
		for index := 1; index < len(arguments); index++ {
			switch arguments[index] {
			case "--name":
				index++
				instance.Name = arguments[index]
			case "--mount-only":
				index++
				paths, err := csv.NewReader(strings.NewReader(arguments[index])).Read()
				if err != nil {
					return err
				}
				mounts := make([]testMount, 0, len(paths))
				for _, path := range paths {
					path = strings.TrimSuffix(path, limaMountWritableSuffix)
					mounts = append(mounts, testMount{Location: path, MountPoint: path, Writable: true})
				}
				instance.Config[limaMountsKey] = mounts
			case "--set":
				index++
				parts := strings.SplitN(arguments[index], " = ", 2)
				var key string
				if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(parts[0], ".["), "]")), &key); err != nil {
					return err
				}
				var value any
				if err := json.Unmarshal([]byte(parts[1]), &value); err != nil {
					return err
				}
				instance.Config[key] = value
			}
		}
		database.VMs[instance.Name] = instance
	case "start", "stop":
		instance, present := database.VMs[name]
		if !present {
			return fmt.Errorf("missing VM %s", name)
		}
		instance.Status = limaStatusRunning
		if operation == "stop" {
			instance.Status = limaStatusStopped
		}
		database.VMs[name] = instance
	case "delete":
		delete(database.VMs, name)
	case "shell":
		if len(arguments) >= 3 && arguments[len(arguments)-3] == guestCommandProbe {
			if _, err := fmt.Fprintln(os.Stdout, "/usr/bin/"+name); err != nil {
				return err
			}
		}
		if stage == "setup" {
			name = arguments[3]
			instance := database.VMs[name]
			instance.SetupCount++
			database.VMs[name] = instance
		}
	default:
		return fmt.Errorf("unexpected operation %s", operation)
	}
	return database.save()
}

func vmFixture(t *testing.T, status string) (string, string, WorkflowOptions) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	name, err := projectVMName(project)
	assert.NilError(t, err)
	binary, err := os.Executable()
	assert.NilError(t, err)
	t.Setenv(vmBinaryEnv, binary)
	t.Setenv(vmDatabaseEnv, filepath.Join(root, "vms.json"))
	t.Setenv(vmFailureEnv, "")
	database := vmDatabase{VMs: map[string]testVM{}}
	if status != "" {
		database.VMs[name] = testVM{Name: name, Status: status, ID: testOriginalID, Config: map[string]any{
			limaMountsKey: []testMount{{Location: project, MountPoint: project, Writable: true}},
		}}
	}
	assert.NilError(t, database.save())
	script, err := os.ReadFile("testdata/recreate/limactl.sh")
	assert.NilError(t, err)
	command := filepath.Join(root, limaCtlCommand)
	assert.NilError(t, os.WriteFile(command, script, 0o755))
	config := DevelopmentConfig{Lima: map[string]any{"cpus": 4, "memory": "8GiB"}, Setup: []SetupCommand{{Command: testSetupCommand}}}
	return project, name, WorkflowOptions{Development: &config, LimaCommand: command, LockDirectory: filepath.Join(root, "locks")}
}

func TestMakeCommandPassesThroughGuestArgumentsAndExitStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		failure  string
		wantCode int
	}{
		{name: "success", wantCode: 0},
		{name: "make failure", failure: "connect", wantCode: 23},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, vmName, options := vmFixture(t, "")
			root := filepath.Dir(project)
			assert.NilError(t, os.WriteFile(filepath.Join(project, projectConfigName), []byte("packages: []\ncopy_git_config: false\n"), 0o600))
			t.Setenv("HOME", root)
			t.Setenv(xdgConfigHomeEnv, filepath.Join(root, "config"))
			t.Setenv("PATH", filepath.Dir(options.LimaCommand)+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(vmFailureEnv, test.failure)

			cli := CLI{ProjectState: true}
			cli.Make.Arguments = []string{"--", "-f", "Makefile", "target with spaces"}
			code, err := runCommand(&cli, makeCommand, project, project)
			assert.NilError(t, err)
			assert.Equal(t, code, test.wantCode)

			database, err := readVMDatabase()
			assert.NilError(t, err)
			assert.DeepEqual(t, database.Operations[len(database.Operations)-1], []string{
				"shell", "--workdir", project, vmName, "make", "-f", "Makefile", "target with spaces",
			})
		})
	}
}

func TestCreateVM(t *testing.T) {
	for _, status := range []string{"", limaStatusStopped, limaStatusRunning, limaStatusBroken} {
		t.Run(status, func(t *testing.T) {
			project, name, options := vmFixture(t, status)
			if status != "" {
				options.Development.EnvPassthrough = []string{"LJA_TEST_UNSET_VARIABLE"}
			}
			instance, err := CreateVM(project, options)
			assert.NilError(t, err)
			assert.Equal(t, instance.Name, name)
			database, err := readVMDatabase()
			assert.NilError(t, err)
			if status != "" {
				assert.Equal(t, len(database.Operations), 0)
				assert.Equal(t, instance.Status, status)
			} else {
				assert.Equal(t, instance.Status, limaStatusRunning)
				assert.Equal(t, database.VMs[name].SetupCount, 1)
				assert.Equal(t, database.VMs[name].Config["cpus"], float64(4))
				assert.Equal(t, database.VMs[name].Config["memory"], "8GiB")
			}
		})
	}
}

func TestRecreateVM(t *testing.T) {
	for _, status := range []string{"", limaStatusStopped, limaStatusRunning, limaStatusBroken} {
		t.Run(status, func(t *testing.T) {
			project, name, options := vmFixture(t, status)
			options.Recreate = true
			instance, err := CreateVM(project, options)
			assert.NilError(t, err)
			assert.Equal(t, instance.Status, limaStatusRunning)
			database, err := readVMDatabase()
			assert.NilError(t, err)
			assert.Equal(t, len(database.VMs), 1)
			assert.Equal(t, database.VMs[name].ID, testReplacementID)
			assert.Equal(t, database.VMs[name].SetupCount, 1)
			if status != "" {
				stages := []string{}
				for _, operation := range database.Operations {
					stages = append(stages, operation[0])
				}
				expected := []string{"rename", "create", "start", "shell", "stop"}
				if status == limaStatusRunning {
					expected = append(expected, "stop")
				}
				expected = append(expected, "rename", "rename", "start", "shell", "delete")
				assert.DeepEqual(t, stages, expected)
			}
		})
	}
}

func TestRecreateFailures(t *testing.T) {
	for _, failure := range []string{"rename-help", "create", "start-candidate", "setup", "stop-candidate", "stop-final", "rename-old-partial", "rename-new-partial", "start-final", "connect", "delete-backup"} {
		t.Run(failure, func(t *testing.T) {
			project, name, options := vmFixture(t, limaStatusRunning)
			t.Setenv(vmFailureEnv, failure)
			options.Recreate = true
			_, err := CreateVM(project, options)
			if failure == "connect" {
				assert.ErrorContains(t, err, "cannot connect")
			} else {
				assert.ErrorContains(t, err, "23")
			}
			database, err := readVMDatabase()
			assert.NilError(t, err)
			switch {
			case strings.HasSuffix(failure, "-partial"):
				assert.Equal(t, len(database.VMs), 3)
				for _, operation := range database.Operations {
					assert.Assert(t, operation[0] != "delete")
				}
			case failure == "delete-backup":
				assert.Equal(t, len(database.VMs), 2)
				assert.Equal(t, database.VMs[name].ID, testReplacementID)
				assert.Equal(t, database.VMs[name].Status, limaStatusRunning)
			default:
				assert.Equal(t, len(database.VMs), 1)
				assert.Equal(t, database.VMs[name].ID, testOriginalID)
				assert.Equal(t, database.VMs[name].Status, limaStatusRunning)
			}
		})
	}
}

func TestRecreateParsing(t *testing.T) {
	for _, command := range []string{createCommandName, "shell", makeCommand, codexAgentName, claudeAgentName, openCodeAgentName, "update", "config", "status", stopCommandName, deleteCommandName} {
		t.Run(command, func(t *testing.T) {
			for _, before := range []bool{true, false} {
				cli := CLI{}
				parser, err := newParser(&cli)
				assert.NilError(t, err)
				arguments := []string{command}
				if before {
					arguments = append([]string{"--recreate"}, arguments...)
				} else {
					arguments = append(arguments, "--recreate")
				}
				if command == "update" {
					arguments = append(arguments, codexAgentName)
				}
				context, err := parser.Parse(arguments)
				assert.NilError(t, err)
				assert.Equal(t, cli.Recreate, true)
				_, err = requestedAgents(&cli, commandName(context))
				if command == "config" || command == "status" || command == stopCommandName || command == deleteCommandName {
					assert.ErrorContains(t, err, "--recreate is only valid")
				} else {
					assert.NilError(t, err)
				}
			}
		})
	}
	cli := CLI{}
	parser, err := newParser(&cli)
	assert.NilError(t, err)
	_, err = parser.Parse([]string{codexAgentName, "--", "--recreate"})
	assert.NilError(t, err)
	assert.Equal(t, cli.Recreate, false)
	assert.DeepEqual(t, forwardedArguments(cli.Codex.Arguments), []string{"--recreate"})
}

func TestMakeParsing(t *testing.T) {
	cli := CLI{}
	parser, err := newParser(&cli)
	assert.NilError(t, err)
	context, err := parser.Parse([]string{makeCommand, "--", "-f", "Makefile", "target with spaces"})
	assert.NilError(t, err)
	assert.Equal(t, commandName(context), makeCommand)
	assert.DeepEqual(t, forwardedArguments(cli.Make.Arguments), []string{"-f", "Makefile", "target with spaces"})

	cli.WithAgent = []string{codexAgentName}
	_, err = requestedAgents(&cli, makeCommand)
	assert.ErrorContains(t, err, "--with-agent is only valid")
}

func TestRecreateCleanupFailures(t *testing.T) {
	for _, test := range []struct {
		failure, message string
		count            int
		originalStatus   string
	}{
		{"setup|delete", "cannot remove failed replacement", 2, limaStatusRunning},
		{"start-final|delete", "could not be deleted", 2, limaStatusRunning},
		{"start-final|start-final", "restarting it failed", 2, limaStatusStopped},
		{"connect|stop-final", "backup retained", 2, limaStatusRunning},
		{"start-final|rename-new-partial", "may be partial", 3, limaStatusStopped},
	} {
		t.Run(test.failure, func(t *testing.T) {
			project, name, options := vmFixture(t, limaStatusRunning)
			t.Setenv(vmFailureEnv, test.failure)
			options.Recreate = true
			_, err := CreateVM(project, options)
			assert.ErrorContains(t, err, test.message)
			database, err := readVMDatabase()
			assert.NilError(t, err)
			assert.Equal(t, len(database.VMs), test.count)
			assert.Equal(t, database.VMs[name].Status, test.originalStatus)
			foundOriginal := false
			for _, instance := range database.VMs {
				foundOriginal = foundOriginal || instance.ID == testOriginalID
			}
			assert.Assert(t, foundOriginal)
		})
	}
}

func TestRecreateMountChange(t *testing.T) {
	project, name, options := vmFixture(t, limaStatusStopped)
	options.StateRoot = filepath.Join(filepath.Dir(project), "state")
	_, err := CreateVM(project, options)
	assert.ErrorContains(t, err, "mounts do not exactly match")
	options.Recreate = true
	instance, err := CreateVM(project, options)
	assert.NilError(t, err)
	assert.Equal(t, instance.Name, name)
	assert.NilError(t, ValidateProjectMount(instance, project, options.StateRoot))
}

func TestRecreateInstallingIsRejected(t *testing.T) {
	project, name, options := vmFixture(t, limaStatusInstalling)
	options.Recreate = true
	_, err := CreateVM(project, options)
	assert.ErrorContains(t, err, "still installing")
	database, err := readVMDatabase()
	assert.NilError(t, err)
	assert.Equal(t, len(database.Operations), 0)
	assert.Equal(t, database.VMs[name].ID, testOriginalID)
}

func TestPreparationRecreatesOnce(t *testing.T) {
	for _, command := range []string{"prepare", "shell", "update", "agents"} {
		t.Run(command, func(t *testing.T) {
			project, name, options := vmFixture(t, limaStatusRunning)
			host := filepath.Join(filepath.Dir(project), "host")
			assert.NilError(t, os.Mkdir(host, 0o755))
			t.Setenv("HOME", host)
			options.Recreate = true
			var err error
			switch command {
			case "prepare":
				_, err = PrepareVM(project, options)
			case "shell":
				_, err = OpenShell(project, []string{"true"}, project, options)
			case "update":
				_, err = InstallAgent(project, codexAgentName, true, options)
			case "agents":
				_, err = PrepareAgents(project, codexAgentName, nil, []string{}, options)
			}
			assert.NilError(t, err)
			database, err := readVMDatabase()
			assert.NilError(t, err)
			assert.Equal(t, database.VMs[name].SetupCount, 1)
			created := 0
			for _, operation := range database.Operations {
				if operation[0] == createCommandName {
					created++
				}
			}
			assert.Equal(t, created, 1)
		})
	}
}
