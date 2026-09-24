package lja

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"gotest.tools/v3/assert"
)

const (
	vmProcessEnv             = "LJA_TEST_VM_PROCESS"
	vmDatabaseEnv            = "LJA_TEST_VM_DATABASE"
	vmFailureEnv             = "LJA_TEST_VM_FAILURE"
	vmBinaryEnv              = "LJA_TEST_BINARY"
	vmPackageInstallNoopEnv  = "LJA_TEST_PACKAGE_INSTALL_NOOP"
	vmNodeRuntimeModeEnv     = "LJA_TEST_NODE_RUNTIME_MODE"
	vmAgentInstallFailureEnv = "LJA_TEST_AGENT_INSTALL_FAILURE"
	testOriginalID           = "original"
	testReplacementID        = "replacement"
	testSetupCommand         = "test-setup"
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
	VMs                      map[string]testVM
	Operations               [][]string
	CreateInputs             []string
	CreateWorkingDirectories []string
	Failed                   int
	PackageInstallationDone  bool
}

type vmProcessExitError struct {
	code    int
	message string
}

func (err vmProcessExitError) Error() string {
	return err.message
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
		if exitErr, ok := err.(vmProcessExitError); ok {
			fmt.Fprintln(os.Stderr, exitErr.message)
			os.Exit(exitErr.code)
		}
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
	guestArguments := []string{}
	if operation == "shell" {
		guestArgumentIndex := 4
		if len(arguments) > 1 && arguments[1] == limaNoninteractiveFlag {
			guestArgumentIndex++
		}
		if len(arguments) <= guestArgumentIndex {
			return fmt.Errorf("missing fake guest command")
		}
		guestArguments = unwrapGuestPathArguments(arguments[guestArgumentIndex:])
	}
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
		} else if isPackageInstallationCommand(guestArguments) {
			stage = "package-install"
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
		if arguments[len(arguments)-1] == "-" {
			input, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			database.CreateInputs = append(database.CreateInputs, string(input))
			if err := yaml.Unmarshal(input, &instance.Config); err != nil {
				return err
			}
			workingDirectory, err := os.Getwd()
			if err != nil {
				return err
			}
			database.CreateWorkingDirectories = append(database.CreateWorkingDirectories, workingDirectory)
		}
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
		if len(guestArguments) >= 3 && guestArguments[0] == sudoCommand && guestArguments[1] == npmCommand && guestArguments[2] == installCommand && os.Getenv(vmAgentInstallFailureEnv) == "1" {
			fmt.Fprintln(os.Stderr, "npm ERR! code EBADENGINE")
			return vmProcessExitError{code: 23, message: "npm engine requirements are incompatible"}
		}
		if len(guestArguments) == 2 && guestArguments[0] == catCommand && guestArguments[1] == osReleasePath {
			if _, err := fmt.Fprintln(os.Stdout, "ID=ubuntu"); err != nil {
				return err
			}
		}
		if len(guestArguments) == 2 && guestArguments[1] == versionFlag && (guestArguments[0] == nodeCommand || guestArguments[0] == npmCommand) {
			if guestArguments[0] == nodeCommand && os.Getenv(vmNodeRuntimeModeEnv) == "failing" {
				fmt.Fprintln(os.Stderr, "node: cannot execute")
				return vmProcessExitError{code: 23, message: "node runtime failed"}
			}
			if _, err := fmt.Fprintln(os.Stdout, "v22.0.0"); err != nil {
				return err
			}
		}
		if len(guestArguments) > 0 && guestArguments[0] == "dpkg-query" && database.PackageInstallationDone {
			if _, err := fmt.Fprint(os.Stdout, packageInstalledStatus); err != nil {
				return err
			}
		}
		if isPackageInstallationCommand(guestArguments) && os.Getenv(vmPackageInstallNoopEnv) != "1" {
			database.PackageInstallationDone = true
		}
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

func isPackageInstallationCommand(arguments []string) bool {
	arguments = unwrapGuestPathArguments(arguments)
	return len(arguments) >= 4 && arguments[0] == shellCommand && arguments[1] == "-eu" && arguments[2] == shellCommandFlag && strings.Contains(arguments[3], aptGetCommand+" "+installCommand)
}

func unwrapGuestPathArguments(arguments []string) []string {
	if len(arguments) >= 4 && arguments[0] == shellCommand && arguments[1] == shellCommandFlag && arguments[2] == guestPathBootstrapScript && arguments[3] == programName {
		return arguments[4:]
	}
	return arguments
}

func joinedOperations(operations [][]string) string {
	lines := make([]string, 0, len(operations))
	for _, operation := range operations {
		lines = append(lines, strings.Join(operation, " "))
	}
	return strings.Join(lines, "\n")
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

func TestEffectiveDevelopmentPackages(t *testing.T) {
	for _, test := range []struct {
		name          string
		packages      []string
		copyGitConfig bool
		want          []string
	}{
		{name: "configured packages", packages: []string{"make", "ninja-build"}, want: []string{"make", "ninja-build"}},
		{name: "implicit Git", packages: []string{"make"}, copyGitConfig: true, want: []string{"make", gitCommand}},
		{name: "deduplicated Git", packages: []string{"git", "make", "git"}, copyGitConfig: true, want: []string{"git", "make"}},
		{name: "empty without Git copy", copyGitConfig: false, want: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.DeepEqual(t, effectiveDevelopmentPackages(DevelopmentConfig{
				Packages:      test.packages,
				CopyGitConfig: test.copyGitConfig,
			}), test.want)
		})
	}
}

func TestGuestPackageInstallationScript(t *testing.T) {
	for _, test := range []struct {
		name     string
		packages []string
		want     string
	}{
		{name: "one package", packages: []string{"make"}, want: "sudo apt-get update && sudo apt-get install -y 'make'"},
		{name: "multiple packages", packages: []string{"make", "ninja-build"}, want: "sudo apt-get update && sudo apt-get install -y 'make' 'ninja-build'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, guestPackageInstallationScript(test.packages), test.want)
		})
	}
}

func TestPreparationBatchesGuestPackages(t *testing.T) {
	for _, test := range []struct {
		name          string
		packages      []string
		copyGitConfig bool
		wantScript    string
	}{
		{name: "configured packages", packages: []string{"make", "ninja-build"}, wantScript: "sudo apt-get update && sudo apt-get install -y 'make' 'ninja-build'"},
		{name: "implicit Git", packages: []string{"make"}, copyGitConfig: true, wantScript: "sudo apt-get update && sudo apt-get install -y 'make' 'git'"},
		{name: "configured Git is not duplicated", packages: []string{"git", "make"}, copyGitConfig: true, wantScript: "sudo apt-get update && sudo apt-get install -y 'git' 'make'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, vmName, options := vmFixture(t, "")
			root := filepath.Dir(project)
			t.Setenv("HOME", root)
			config := DevelopmentConfig{Packages: test.packages, CopyGitConfig: test.copyGitConfig}
			err := prepareDevelopment(project, vmName, &config, nil, options.LimaCommand)
			assert.NilError(t, err)

			database, err := readVMDatabase()
			assert.NilError(t, err)
			installations := make([][]string, 0)
			for _, operation := range database.Operations {
				if len(operation) >= 8 && isPackageInstallationCommand(operation[4:]) {
					installations = append(installations, operation)
				}
			}
			assert.Equal(t, len(installations), 1)
			guestArguments := unwrapGuestPathArguments(installations[0][4:])
			assert.Equal(t, guestArguments[3], test.wantScript)
		})
	}
}

func TestPreparationKeepsPackageInstallationBeforeSetup(t *testing.T) {
	project, vmName, options := vmFixture(t, limaStatusRunning)
	config := DevelopmentConfig{
		Packages: []string{"make"},
		Setup:    []SetupCommand{{Command: testSetupCommand}},
	}
	assert.NilError(t, prepareDevelopment(project, vmName, &config, nil, options.LimaCommand))

	database, err := readVMDatabase()
	assert.NilError(t, err)
	packageIndex := -1
	setupIndex := -1
	for index, operation := range database.Operations {
		if len(operation) >= 5 && isPackageInstallationCommand(operation[4:]) {
			packageIndex = index
		}
		if len(operation) > 0 && operation[len(operation)-1] == testSetupCommand {
			setupIndex = index
		}
	}
	assert.Assert(t, packageIndex >= 0)
	assert.Assert(t, setupIndex >= 0)
	assert.Assert(t, packageIndex < setupIndex)
}

func TestPreparationSkipsReadyAndEmptyGuestPackages(t *testing.T) {
	for _, test := range []struct {
		name               string
		packages           []string
		packageInstallDone bool
		wantPackageQueries int
		wantInstallations  int
	}{
		{name: "already installed", packages: []string{"make", "ninja-build"}, packageInstallDone: true, wantPackageQueries: 2},
		{name: "empty list", packages: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, vmName, options := vmFixture(t, "")
			database, err := readVMDatabase()
			assert.NilError(t, err)
			database.PackageInstallationDone = test.packageInstallDone
			assert.NilError(t, database.save())
			config := DevelopmentConfig{Packages: test.packages}
			err = prepareDevelopment(project, vmName, &config, nil, options.LimaCommand)
			assert.NilError(t, err)

			database, err = readVMDatabase()
			assert.NilError(t, err)
			packageQueries := 0
			installations := 0
			for _, operation := range database.Operations {
				guestArguments := unwrapGuestPathArguments(operation[4:])
				if len(guestArguments) >= 1 && guestArguments[0] == "dpkg-query" {
					packageQueries++
				}
				if len(operation) >= 8 && isPackageInstallationCommand(operation[4:]) {
					installations++
				}
			}
			assert.Equal(t, packageQueries, test.wantPackageQueries)
			assert.Equal(t, installations, test.wantInstallations)
		})
	}
}

func TestPreparationReportsBatchPackageFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure string
		noop    bool
		want    string
	}{
		{name: "installation failure", failure: "package-install", want: "cannot install dependencies make, ninja-build with ubuntu/debian backend"},
		{name: "verification failure", noop: true, want: "installation did not provide the requested package"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, vmName, options := vmFixture(t, "")
			t.Setenv(vmFailureEnv, test.failure)
			if test.noop {
				t.Setenv(vmPackageInstallNoopEnv, "1")
			}
			config := DevelopmentConfig{Packages: []string{"make", "ninja-build"}}
			err := prepareDevelopment(project, vmName, &config, nil, options.LimaCommand)
			assert.ErrorContains(t, err, test.want)
		})
	}
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
				"shell", "--workdir", project, vmName, shellCommand, shellCommandFlag, guestPathBootstrapScript, programName,
				"make", "-f", "Makefile", "target with spaces",
			})
		})
	}
}

func TestShellAndMakeOnlyInstallNodeWhenRequested(t *testing.T) {
	for _, test := range []struct {
		name            string
		command         string
		packages        string
		wantNodeInstall bool
	}{
		{name: "shell without agents", command: "shell", packages: "[]"},
		{name: "make without agents", command: makeCommand, packages: "[]"},
		{name: "shell with requested Node packages", command: "shell", packages: "[nodejs, npm]", wantNodeInstall: true},
		{name: "make with requested Node packages", command: makeCommand, packages: "[nodejs, npm]", wantNodeInstall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, _, options := vmFixture(t, limaStatusRunning)
			root := filepath.Dir(project)
			content := "packages: " + test.packages + "\ncopy_git_config: false\n"
			assert.NilError(t, os.WriteFile(filepath.Join(project, projectConfigName), []byte(content), 0o600))
			t.Setenv("HOME", root)
			t.Setenv(xdgConfigHomeEnv, filepath.Join(root, "config"))
			t.Setenv("PATH", filepath.Dir(options.LimaCommand)+string(os.PathListSeparator)+os.Getenv("PATH"))

			cli := CLI{ProjectState: true}
			if test.command == "shell" {
				cli.Shell.Arguments = []string{"--", "true"}
			} else {
				cli.Make.Arguments = []string{"--", "true"}
			}
			code, err := runCommand(&cli, test.command, project, project)
			assert.NilError(t, err)
			assert.Equal(t, code, 0)

			database, err := readVMDatabase()
			assert.NilError(t, err)
			operationText := joinedOperations(database.Operations)
			assert.Equal(t, strings.Contains(operationText, "nodejs") && strings.Contains(operationText, "npm"), test.wantNodeInstall)
			assert.Assert(t, !strings.Contains(operationText, "snap"))
		})
	}
}

func TestInteractiveShellUsesGuestPathBootstrap(t *testing.T) {
	project, vmName, options := vmFixture(t, limaStatusRunning)
	code, err := OpenShell(project, nil, "", options)
	assert.NilError(t, err)
	assert.Equal(t, code, 0)

	database, err := readVMDatabase()
	assert.NilError(t, err)
	assert.DeepEqual(t, database.Operations[len(database.Operations)-1], []string{
		"shell", "--workdir", project, vmName, shellCommand, shellCommandFlag, guestPathBootstrapScript, programName,
		shellCommand, shellCommandFlag, "exec \"${SHELL:-/bin/sh}\" -l",
	})
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
				assert.Equal(t, database.VMs[name].Config[limaBaseKey], limaDefaultTemplate)
				assert.DeepEqual(t, database.CreateWorkingDirectories, []string{project})
				assert.DeepEqual(t, database.Operations[0], []string{
					createCommandName, limaNoninteractiveFlag, "--name", name,
					"--mount-only", project + limaMountWritableSuffix, "-",
				})
			}
		})
	}
}

func TestCreationAndRecreationUseGoldenInputAndMountArguments(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      string
		recreate    bool
		input       string
		expected    string
		sharedState bool
	}{
		{
			name:     "creation with Fedora template",
			input:    "fedora/input.yaml",
			expected: "fedora/expected.yaml",
		},
		{
			name:        "recreation with relative template and shared state",
			status:      limaStatusRunning,
			recreate:    true,
			input:       "relative-reference/input.yaml",
			expected:    "relative-reference/expected.yaml",
			sharedState: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, name, options := vmFixture(t, test.status)
			if test.sharedState {
				options.StateRoot = filepath.Join(filepath.Dir(project), "state")
			}
			source, err := decodeDevelopmentConfig(readLimaCreationFixture(t, test.input), true)
			assert.NilError(t, err)
			options.Development = &DevelopmentConfig{Lima: source.Lima}
			options.Recreate = test.recreate

			_, err = CreateVM(project, options)
			assert.NilError(t, err)
			database, err := readVMDatabase()
			assert.NilError(t, err)
			assert.Equal(t, len(database.CreateInputs), 1)
			assert.Equal(t, database.CreateInputs[0], string(readLimaCreationFixture(t, test.expected)))
			assert.DeepEqual(t, database.CreateWorkingDirectories, []string{project})

			createOperations := make([][]string, 0, 1)
			for _, operation := range database.Operations {
				if len(operation) > 0 && operation[0] == createCommandName {
					createOperations = append(createOperations, operation)
				}
			}
			assert.Equal(t, len(createOperations), 1)
			creationName := name
			if test.recreate {
				assert.Assert(t, strings.HasPrefix(createOperations[0][3], replacementNamePrefix))
				creationName = createOperations[0][3]
			}
			mountPaths, err := ExpectedMountPaths(project, options.StateRoot)
			assert.NilError(t, err)
			mountArguments, err := MountArguments(mountPaths)
			assert.NilError(t, err)
			expectedOperation := []string{createCommandName, limaNoninteractiveFlag, "--name", creationName}
			expectedOperation = append(expectedOperation, mountArguments...)
			expectedOperation = append(expectedOperation, "-")
			assert.DeepEqual(t, createOperations[0], expectedOperation)
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

func TestRecreatePublishesConfiguredAgentWrappersUnderFinalName(t *testing.T) {
	project, name, options := vmFixture(t, limaStatusRunning)
	config := *options.Development
	config.Agents = []string{codexAgentName, claudeAgentName}
	options.Development = &config
	options.Recreate = true

	_, err := PrepareVM(project, options)
	assert.NilError(t, err)

	database, err := readVMDatabase()
	assert.NilError(t, err)
	finalWrapperPath := agentWrapperDirectory(name)
	for _, operation := range database.Operations {
		if len(operation) == 0 || !strings.Contains(strings.Join(operation, " "), "wrapper") {
			continue
		}
		assert.Assert(t, strings.Contains(strings.Join(operation, " "), finalWrapperPath))
	}
	assert.Assert(t, strings.Contains(strings.Join(database.Operations[len(database.Operations)-1], " "), finalWrapperPath))
}

func TestUpdateConfiguredAgentInstallsOnce(t *testing.T) {
	project, _, options := vmFixture(t, limaStatusRunning)
	config := DevelopmentConfig{Agents: []string{codexAgentName}}
	options.Development = &config

	_, err := InstallAgent(project, codexAgentName, true, options)
	assert.NilError(t, err)

	database, err := readVMDatabase()
	assert.NilError(t, err)
	installations := 0
	for _, operation := range database.Operations {
		if strings.Contains(strings.Join(operation, " "), "npm install --engine-strict -g @openai/codex") {
			installations++
		}
	}
	assert.Equal(t, installations, 1)
}

func TestAgentInstallationReportsRuntimeFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		environment string
		value       string
		want        []string
	}{
		{
			name:        "failing node",
			environment: vmNodeRuntimeModeEnv,
			value:       "failing",
			want:        []string{"guest executable node is present but failed", "node runtime failed"},
		},
		{
			name:        "incompatible engine",
			environment: vmAgentInstallFailureEnv,
			value:       "1",
			want: []string{
				"EBADENGINE",
				"node=v22.0.0 npm=v22.0.0",
				"newer template",
				"custom provisioning",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, _, options := vmFixture(t, limaStatusRunning)
			t.Setenv(test.environment, test.value)

			_, err := InstallAgent(project, codexAgentName, true, options)
			for _, want := range test.want {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}
