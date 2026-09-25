package lja

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

func DiscoverProject(directory, limactlCommand string) (string, error) {
	canonicalDirectory, err := canonicalProjectPath(directory)
	if err != nil {
		return "", err
	}
	policy, err := newProjectDirectoryPolicy()
	if err != nil {
		return "", err
	}
	return policy.discover(canonicalDirectory, limactlCommand)
}

func (policy projectDirectoryPolicy) discover(canonicalDirectory, limactlCommand string) (string, error) {
	vmNames := map[string]bool(nil)
	for candidate := canonicalDirectory; ; candidate = filepath.Dir(candidate) {
		reason, err := policy.rejection(candidate)
		if err != nil {
			return "", err
		}
		if reason != "" {
			if candidate == canonicalDirectory {
				return "", ljaError("%s: %s", reason, candidate)
			}
			slog.Debug("stopping project discovery", "directory", candidate, "reason", reason)
			return canonicalDirectory, nil
		}
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

func discoverProject(directory, limactlCommand string) (string, error) {
	return DiscoverProject(directory, limactlCommand)
}

func ShowStatus(project, stateRoot, limactlCommand string) error {
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
	if _, err := fmt.Printf("project: %s\nstate: %s\nvm: %s\nstatus: %s\n", canonicalProject, selectedState, vmName, status); err != nil {
		return ljaError("cannot report project status: %w", err)
	}
	return nil
}

func StopVM(project, stateRoot, limactlCommand, lockDirectory string) error {
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return err
	}
	vmName, err := projectVMName(canonicalProject)
	if err != nil {
		return err
	}
	return withAdvisoryLock(AdvisoryLockOptions{
		Project:       canonicalProject,
		VMName:        vmName,
		StateRoot:     stateRoot,
		LockDirectory: lockDirectory,
	}, func(string) error {
		instance, inspectErr := inspectLima(vmName, limactlCommand)
		if inspectErr != nil {
			return inspectErr
		}
		if instance == nil {
			return reportVMStatus(vmName, "Absent")
		}
		if err := validateProjectMount(*instance, canonicalProject, stateRoot); err != nil {
			return err
		}
		if instance.Status == limaStatusStopped {
			return reportVMStatus(vmName, "Stopped")
		}
		if instance.Status != limaStatusRunning {
			return ljaError("VM %s is in incompatible state %s", vmName, instance.Status)
		}
		slog.Info("stopping VM", "vm", vmName)
		options := defaultProcessOptions(limactlCommand)
		if _, err := runLima([]string{stopCommandName, vmName}, options); err != nil {
			return err
		}
		return reportVMStatus(vmName, "Stopped")
	})
}
