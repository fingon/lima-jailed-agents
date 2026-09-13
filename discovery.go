package lja

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

func DiscoverProject(directory string, limactlCommand string) (string, error) {
	canonicalDirectory, err := canonicalProjectPath(directory)
	if err != nil {
		return "", err
	}
	vmNames := map[string]bool(nil)
	for candidate := canonicalDirectory; ; candidate = filepath.Dir(candidate) {
		configPath := filepath.Join(candidate, projectConfigName)
		info, statErr := os.Stat(configPath)
		hasConfig := false
		if statErr == nil {
			hasConfig = info.Mode().IsRegular()
		} else if !os.IsNotExist(statErr) {
			return "", ljaError("cannot inspect project configuration %s: %w", configPath, statErr)
		}
		if hasConfig {
			return candidate, nil
		}
		if vmNames == nil {
			options := defaultProcessOptions(limactlCommand)
			options.captureOutput = true
			result, runErr := runLima(limaListOperation(), options)
			if runErr != nil {
				return "", runErr
			}
			instances, parseErr := ParseLimaInstances(result.Stdout)
			if parseErr != nil {
				return "", parseErr
			}
			vmNames = make(map[string]bool, len(instances))
			for _, instance := range instances {
				vmNames[instance.Name] = true
			}
		}
		vmName, nameErr := projectVMName(candidate)
		if nameErr != nil {
			return "", nameErr
		}
		if vmNames[vmName] {
			return candidate, nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return canonicalDirectory, nil
		}
	}
}

func discoverProject(directory string, limactlCommand string) (string, error) {
	return DiscoverProject(directory, limactlCommand)
}

func ShowStatus(project string, stateRoot string, limactlCommand string) error {
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return err
	}
	vmName, err := projectVMName(canonicalProject)
	if err != nil {
		return err
	}
	instance, err := inspectLima(vmName, limactlCommand)
	if err != nil {
		return err
	}
	status := "Absent"
	if instance != nil {
		if err := validateProjectMount(*instance, canonicalProject, stateRoot); err != nil {
			return err
		}
		status = instance.Status
	}
	selectedState := stateRoot
	if selectedState == "" {
		selectedState = canonicalProject
	}
	fmt.Printf("project: %s\nstate: %s\nvm: %s\nstatus: %s\n", canonicalProject, selectedState, vmName, status)
	return nil
}

func showStatus(project string, stateRoot string, limactlCommand string) error {
	return ShowStatus(project, stateRoot, limactlCommand)
}

func StopVM(project string, stateRoot string, limactlCommand string, lockDirectory string) error {
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return err
	}
	vmName, err := projectVMName(canonicalProject)
	if err != nil {
		return err
	}
	return withAdvisoryLock(canonicalProject, vmName, stateRoot, lockDirectory, func(string) error {
		instance, inspectErr := inspectLima(vmName, limactlCommand)
		if inspectErr != nil {
			return inspectErr
		}
		if instance == nil {
			fmt.Printf("vm: %s\nstatus: Absent\n", vmName)
			return nil
		}
		if err := validateProjectMount(*instance, canonicalProject, stateRoot); err != nil {
			return err
		}
		if instance.Status == limaStatusStopped {
			fmt.Printf("vm: %s\nstatus: Stopped\n", vmName)
			return nil
		}
		if instance.Status != limaStatusRunning {
			return ljaError("VM %s is in incompatible state %s", vmName, instance.Status)
		}
		slog.Info("stopping VM", "vm", vmName)
		options := defaultProcessOptions(limactlCommand)
		if _, err := runLima([]string{"stop", vmName}, options); err != nil {
			return err
		}
		fmt.Printf("vm: %s\nstatus: Stopped\n", vmName)
		return nil
	})
}

func stopVM(project string, stateRoot string, limactlCommand string, lockDirectory string) error {
	return StopVM(project, stateRoot, limactlCommand, lockDirectory)
}
