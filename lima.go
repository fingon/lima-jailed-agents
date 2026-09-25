package lja

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

const (
	limaMountMismatchMessage = "VM %s mounts do not exactly match selected writable paths: %s. For an existing project-local VM, use %s. To change storage, use lja create --recreate for VM %s; host state is not migrated automatically."
	limaListArguments        = "list"
	limaListAllFields        = "--all-fields"
	limaListFormatFlag       = "--format"
	limaListFormatJSON       = "json"
	limaMountOnlyFlag        = "--mount-only"
	limaMountWritableSuffix  = ":w"
	limaConfigKey            = "config"
	limaNameFlag             = "--name"
	limaStatusKey            = "status"
	limaMemoryKey            = "memory"
	limaShellOperation       = "shell"
	limaStatusUninitialized  = "Uninitialized"
	limaStatusInstalling     = "Installing"
	limaStatusBroken         = "Broken"
	limaStatusStopped        = "Stopped"
	limaStatusRunning        = "Running"
	jsonNullValue            = "null"
)

var knownLimaStatuses = map[string]bool{
	limaStatusUninitialized: true,
	limaStatusInstalling:    true,
	limaStatusBroken:        true,
	limaStatusStopped:       true,
	limaStatusRunning:       true,
}

type LimaMount struct {
	Location   string
	MountPoint string
	Writable   bool
}

type LimaInstance struct {
	Name   string
	Status string
	Mounts []LimaMount
}

func limaListOperation() []string {
	return []string{limaListArguments, limaListAllFields, limaListFormatFlag, limaListFormatJSON}
}

func rawObject(value json.RawMessage, context string) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, ljaError("malformed Lima JSON: %s must be an object", context)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		if err != nil {
			return nil, ljaError("malformed Lima JSON: %s: %w", context, err)
		}
		return nil, ljaError("malformed Lima JSON: %s must be an object", context)
	}
	return object, nil
}

func requiredJSONString(object map[string]json.RawMessage, name, context string) (string, error) {
	raw, present := object[name]
	if !present {
		return "", ljaError("malformed Lima JSON: %s has no %s", context, name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", ljaError("malformed Lima JSON: %s has no %s", context, name)
	}
	return value, nil
}

func ParseLimaInstances(payload []byte) ([]LimaInstance, error) {
	if strings.TrimSpace(string(payload)) == "" {
		return []LimaInstance{}, nil
	}
	var decoded json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		lines := bytes.Split(payload, []byte("\n"))
		decodedValues := make([]json.RawMessage, 0)
		for lineIndex, line := range lines {
			if strings.TrimSpace(string(line)) == "" {
				continue
			}
			var value json.RawMessage
			if lineErr := json.Unmarshal(line, &value); lineErr != nil {
				return nil, ljaError("malformed Lima JSON on line %d: %w", lineIndex+1, lineErr)
			}
			decodedValues = append(decodedValues, value)
		}
		if len(decodedValues) == 0 {
			return nil, ljaError("malformed Lima JSON: %w", err)
		}
		joined, marshalErr := json.Marshal(decodedValues)
		if marshalErr != nil {
			return nil, ljaError("malformed Lima JSON: %w", marshalErr)
		}
		decoded = joined
	}
	trimmed := bytes.TrimSpace(decoded)
	rawInstances := make([]json.RawMessage, 0)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rawInstances); err != nil {
			return nil, ljaError("malformed Lima JSON: %w", err)
		}
	} else {
		rawInstances = append(rawInstances, trimmed)
	}
	instances := make([]LimaInstance, 0, len(rawInstances))
	for instanceIndex, rawInstance := range rawInstances {
		instance, err := rawObject(rawInstance, fmt.Sprintf("instance %d", instanceIndex))
		if err != nil {
			return nil, err
		}
		name, err := requiredJSONString(instance, "name", fmt.Sprintf("instance %d", instanceIndex))
		if err != nil {
			return nil, err
		}
		status, err := requiredJSONString(instance, limaStatusKey, "instance "+name)
		if err != nil {
			return nil, err
		}
		rawConfig, present := instance[limaConfigKey]
		if !present {
			return nil, ljaError("malformed Lima JSON: instance %s.config must be an object", name)
		}
		config, err := rawObject(rawConfig, "instance "+name+".config")
		if err != nil {
			return nil, err
		}
		rawMounts, present := config[limaMountsKey]
		if !present || len(bytes.TrimSpace(rawMounts)) == 0 || bytes.TrimSpace(rawMounts)[0] != '[' {
			return nil, ljaError("malformed Lima JSON: instance %s.config.mounts must be an array", name)
		}
		var mountsRaw []json.RawMessage
		if err := json.Unmarshal(rawMounts, &mountsRaw); err != nil {
			return nil, ljaError("malformed Lima JSON: instance %s.config.mounts must be an array: %w", name, err)
		}
		mounts := make([]LimaMount, 0, len(mountsRaw))
		for mountIndex, rawMount := range mountsRaw {
			mount, err := rawObject(rawMount, fmt.Sprintf("instance %s.config.mounts[%d]", name, mountIndex))
			if err != nil {
				return nil, err
			}
			location, err := requiredJSONString(mount, "location", fmt.Sprintf("instance %s mount %d", name, mountIndex))
			if err != nil {
				return nil, err
			}
			mountPoint := location
			if rawMountPoint, found := mount["mountPoint"]; found && string(bytes.TrimSpace(rawMountPoint)) != jsonNullValue {
				if decodeErr := json.Unmarshal(rawMountPoint, &mountPoint); decodeErr != nil || mountPoint == "" {
					return nil, ljaError("malformed Lima JSON: instance %s mount %d has an invalid mount point", name, mountIndex)
				}
			}
			writable := false
			if rawWritable, found := mount["writable"]; found && string(bytes.TrimSpace(rawWritable)) != "null" {
				if decodeErr := json.Unmarshal(rawWritable, &writable); decodeErr != nil {
					return nil, ljaError("malformed Lima JSON: instance %s mount %d has an invalid writable value", name, mountIndex)
				}
			}
			mounts = append(mounts, LimaMount{Location: location, MountPoint: mountPoint, Writable: writable})
		}
		instances = append(instances, LimaInstance{Name: name, Status: status, Mounts: mounts})
	}
	return instances, nil
}

func InspectLima(vmName, limactlCommand string) (*LimaInstance, error) {
	options := defaultProcessOptions(limactlCommand)
	options.captureOutput = true
	result, err := runLima(limaListOperation(), options)
	if err != nil {
		return nil, err
	}
	instances, err := ParseLimaInstances(result.Stdout)
	if err != nil {
		return nil, err
	}
	for index := range instances {
		instance := instances[index]
		if instance.Name != vmName {
			continue
		}
		if !knownLimaStatuses[instance.Status] {
			return nil, ljaError("incompatible Lima state for VM %s: %s", vmName, instance.Status)
		}
		return &instance, nil
	}
	return nil, nil
}

func inspectLima(vmName, limactlCommand string) (*LimaInstance, error) {
	return InspectLima(vmName, limactlCommand)
}

func ExpectedMountPaths(project, stateRoot string) ([]string, error) {
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return nil, err
	}
	if stateRoot == "" {
		return []string{canonicalProject}, nil
	}
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return nil, err
	}
	if canonicalState == canonicalProject {
		return []string{canonicalProject}, nil
	}
	projectWithin, err := pathIsWithin(canonicalState, canonicalProject)
	if err != nil {
		return nil, err
	}
	stateWithin, err := pathIsWithin(canonicalProject, canonicalState)
	if err != nil {
		return nil, err
	}
	if projectWithin || stateWithin {
		return nil, ljaError("shared state root overlaps project: %s", canonicalState)
	}
	return []string{canonicalProject, canonicalState}, nil
}

func expectedMountPaths(project, stateRoot string) ([]string, error) {
	return ExpectedMountPaths(project, stateRoot)
}

func canonicalHostPath(path string) (string, error) {
	return canonicalPath(path)
}

func ValidateProjectMount(instance LimaInstance, project, stateRoot string) error {
	expected, err := ExpectedMountPaths(project, stateRoot)
	if err != nil {
		return err
	}
	type mountKey struct {
		location   string
		mountPoint string
		writable   bool
	}
	actual := make([]mountKey, 0, len(instance.Mounts))
	for _, mount := range instance.Mounts {
		location, err := canonicalHostPath(mount.Location)
		if err != nil {
			return err
		}
		actual = append(actual, mountKey{location: location, mountPoint: mount.MountPoint, writable: mount.Writable})
	}
	expectedSet := make(map[mountKey]bool, len(expected))
	for _, path := range expected {
		expectedSet[mountKey{location: path, mountPoint: path, writable: true}] = true
	}
	actualSet := make(map[mountKey]bool, len(actual))
	for _, mount := range actual {
		actualSet[mount] = true
	}
	if len(actual) != len(expected) || len(actualSet) != len(expectedSet) {
		return ljaError(limaMountMismatchMessage, instance.Name, strings.Join(expected, ", "), projectStateFlag, instance.Name)
	}
	for mount := range expectedSet {
		if !actualSet[mount] {
			return ljaError(limaMountMismatchMessage, instance.Name, strings.Join(expected, ", "), projectStateFlag, instance.Name)
		}
	}
	return nil
}

func validateProjectMount(instance LimaInstance, project, stateRoot string) error {
	return ValidateProjectMount(instance, project, stateRoot)
}

func MountArguments(paths []string) ([]string, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		values = append(values, path+limaMountWritableSuffix)
	}
	if err := writer.Write(values); err != nil {
		return nil, ljaError("cannot encode Lima mount paths: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, ljaError("cannot encode Lima mount paths: %w", err)
	}
	encoded := strings.TrimSuffix(buffer.String(), "\n")
	return []string{limaMountOnlyFlag, encoded}, nil
}

func mountArguments(paths []string) ([]string, error) {
	return MountArguments(paths)
}

type namedVMPreparationOptions struct {
	project         string
	vmName          string
	workflowOptions WorkflowOptions
	prepareWrappers bool
}

func prepareNamedVMLocked(options namedVMPreparationOptions) (LimaInstance, error) {
	project := options.project
	vmName := options.vmName
	workflowOptions := options.workflowOptions
	stateRoot := workflowOptions.StateRoot
	limactlCommand := workflowOptions.limaCommand()
	if err := validateProjectDirectory(project); err != nil {
		return LimaInstance{}, err
	}
	mountPaths, err := expectedMountPaths(project, stateRoot)
	if err != nil {
		return LimaInstance{}, err
	}
	instance, err := inspectLima(vmName, limactlCommand)
	if err != nil {
		return LimaInstance{}, err
	}
	if instance != nil {
		if err := validateProjectMount(*instance, project, stateRoot); err != nil {
			return LimaInstance{}, err
		}
	}
	if len(mountPaths) > 1 {
		if err := ensurePrivateDirectory(mountPaths[1]); err != nil {
			return LimaInstance{}, err
		}
	}
	if instance == nil {
		slog.Info("creating VM", "vm", vmName, "project", project)
		mountArguments, err := mountArguments(mountPaths)
		if err != nil {
			return LimaInstance{}, err
		}
		arguments := []string{createCommandName, limaNoninteractiveFlag, limaNameFlag, vmName}
		creationInput, err := limaCreationInput(developmentConfigOrDefault(workflowOptions.Development).Lima)
		if err != nil {
			return LimaInstance{}, err
		}
		arguments = append(arguments, mountArguments...)
		arguments = append(arguments, "-")
		options := defaultProcessOptions(limactlCommand)
		options.workingDirectory = project
		options.hasInput = true
		options.inputData = creationInput
		if _, err := runLima(arguments, options); err != nil {
			return LimaInstance{}, err
		}
		instance, err = inspectLima(vmName, limactlCommand)
		if err != nil {
			return LimaInstance{}, err
		}
		if instance == nil {
			return LimaInstance{}, ljaError("Lima did not report newly created VM %s", vmName)
		}
	}
	if err := validateProjectMount(*instance, project, stateRoot); err != nil {
		return LimaInstance{}, err
	}
	if instance.Status == limaStatusStopped {
		slog.Info("starting VM", "vm", vmName)
		options := defaultProcessOptions(limactlCommand)
		if _, err := runLima([]string{"start", vmName}, options); err != nil {
			return LimaInstance{}, err
		}
	} else if instance.Status != limaStatusRunning {
		return LimaInstance{}, ljaError("VM %s is in incompatible state %s", vmName, instance.Status)
	}
	instance, err = inspectLima(vmName, limactlCommand)
	if err != nil {
		return LimaInstance{}, err
	}
	if instance == nil {
		return LimaInstance{}, ljaError("Lima VM %s disappeared during preparation", vmName)
	}
	if err := validateProjectMount(*instance, project, stateRoot); err != nil {
		return LimaInstance{}, err
	}
	if instance.Status != limaStatusRunning {
		return LimaInstance{}, ljaError("VM %s did not reach Running state; current state is %s", vmName, instance.Status)
	}
	if err := prepareDevelopment(developmentPreparationOptions{
		project:        project,
		vmName:         vmName,
		config:         workflowOptions.Development,
		environment:    workflowOptions.Environment,
		limactlCommand: limactlCommand,
		gpg:            workflowOptions.gpg,
		github:         workflowOptions.github,
	}); err != nil {
		return LimaInstance{}, err
	}
	if err := prepareConfiguredAgentsLocked(workflowOptions.agentPreparation(project, vmName, options.prepareWrappers, workflowOptions.agentUpdateName)); err != nil {
		return LimaInstance{}, err
	}
	return *instance, nil
}

func ljaInstances(limactlCommand string) ([]LimaInstance, error) {
	options := defaultProcessOptions(limactlCommand)
	options.captureOutput = true
	result, err := runLima(limaListOperation(), options)
	if err != nil {
		return nil, err
	}
	instances, err := ParseLimaInstances(result.Stdout)
	if err != nil {
		return nil, err
	}
	selected := make([]LimaInstance, 0)
	for _, instance := range instances {
		if !strings.HasPrefix(instance.Name, vmNamePrefix) {
			continue
		}
		if !knownLimaStatuses[instance.Status] {
			return nil, ljaError("incompatible Lima state for VM %s: %s", instance.Name, instance.Status)
		}
		selected = append(selected, instance)
	}
	return selected, nil
}

func StopAllVMs(limactlCommand string) error {
	instances, err := ljaInstances(limactlCommand)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if instance.Status != limaStatusStopped && instance.Status != limaStatusRunning {
			return ljaError("VM %s is in incompatible state %s", instance.Name, instance.Status)
		}
	}
	for _, instance := range instances {
		status := limaStatusStopped
		if instance.Status == limaStatusRunning {
			slog.Info("stopping VM", "vm", instance.Name)
			options := defaultProcessOptions(limactlCommand)
			if _, err := runLima([]string{stopCommandName, instance.Name}, options); err != nil {
				return err
			}
		}
		if err := reportVMStatus(instance.Name, status); err != nil {
			return err
		}
	}
	return nil
}

func DeleteVM(project, limactlCommand string) error {
	vmName, err := projectVMName(project)
	if err != nil {
		return err
	}
	instance, err := inspectLima(vmName, limactlCommand)
	if err != nil {
		return err
	}
	if instance != nil {
		return instance.delete(limactlCommand)
	}
	return reportAbsentVM(vmName)
}

func DeleteAllVMs(limactlCommand string) error {
	instances, err := ljaInstances(limactlCommand)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if err := instance.delete(limactlCommand); err != nil {
			return err
		}
	}
	return nil
}

func (instance LimaInstance) delete(limactlCommand string) error {
	slog.Info("deleting VM", "vm", instance.Name)
	options := defaultProcessOptions(limactlCommand)
	if _, err := runLima([]string{deleteCommandName, "-f", instance.Name}, options); err != nil {
		return err
	}
	return reportAbsentVM(instance.Name)
}

func reportAbsentVM(vmName string) error {
	if _, err := fmt.Printf("vm: %s\nstatus: Absent\n", vmName); err != nil {
		return ljaError("cannot report deleted VM %s: %w", vmName, err)
	}
	return nil
}

func reportVMStatus(vmName, status string) error {
	if _, err := fmt.Printf("vm: %s\nstatus: %s\n", vmName, status); err != nil {
		return ljaError("cannot report VM %s status: %w", vmName, err)
	}
	return nil
}
