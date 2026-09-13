package lja

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

func withAdvisoryLock(project string, vmName string, stateRoot string, lockDirectory string, function func(string) error) (returnErr error) {
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return err
	}
	if lockDirectory == "" {
		lockDirectory, err = defaultLockDirectory(canonicalProject, stateRoot)
		if err != nil {
			return err
		}
	}
	canonicalLockDirectory, err := canonicalPath(lockDirectory)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(canonicalLockDirectory, vmName+lockFileSuffix)
	canonicalLockPath, err := canonicalPath(lockPath)
	if err != nil {
		return err
	}
	mounts, err := expectedMountPaths(canonicalProject, stateRoot)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		within, withinErr := pathIsWithin(canonicalLockPath, mount)
		if withinErr != nil {
			return withinErr
		}
		if within {
			return ljaError("host lock path would be inside project or state mount: %s", canonicalLockPath)
		}
	}
	if err := os.MkdirAll(canonicalLockDirectory, privateStateDirectoryMode); err != nil {
		return ljaError("cannot open host lock %s: %w", canonicalLockPath, err)
	}
	lockFile, err := os.OpenFile(canonicalLockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return ljaError("cannot open host lock %s: %w", canonicalLockPath, err)
	}
	locked := false
	defer func() {
		if locked {
			if unlockErr := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); unlockErr != nil {
				returnErr = errors.Join(returnErr, ljaError("cannot unlock host lock %s: %w", canonicalLockPath, unlockErr))
			}
		}
		if closeErr := lockFile.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, ljaError("cannot close host lock %s: %w", canonicalLockPath, closeErr))
		}
	}()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return ljaError("cannot lock host lock %s: %w", canonicalLockPath, err)
	}
	locked = true
	if err := function(canonicalLockPath); err != nil {
		return err
	}
	return nil
}

func AdvisoryLock(project string, vmName string, stateRoot string, lockDirectory string, function func(string) error) error {
	return withAdvisoryLock(project, vmName, stateRoot, lockDirectory, function)
}

func resolveStateRoot(project string, stateDirectory *string, projectState bool, environment map[string]string) (string, error) {
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return "", err
	}
	if projectState {
		return canonicalProject, nil
	}
	if environment == nil {
		environment = environmentMap()
	}
	var root string
	if stateDirectory != nil {
		if *stateDirectory == "" {
			return "", ljaError("host environment %s must be a non-empty path", stateDirectoryEnv)
		}
		root, err = configuredHostDirectory(map[string]string{stateDirectoryEnv: *stateDirectory}, stateDirectoryEnv, canonicalProject)
	} else if _, present := environment[stateDirectoryEnv]; present {
		root, err = configuredHostDirectory(environment, stateDirectoryEnv, canonicalProject)
	} else {
		home, homeErr := homeDirectory()
		if homeErr != nil {
			return "", homeErr
		}
		dataRoot := filepath.Join(home, defaultDataDirectory)
		if _, present := environment[xdgDataHomeEnv]; present {
			dataRoot, err = configuredHostDirectory(environment, xdgDataHomeEnv, dataRoot)
			if err != nil {
				return "", err
			}
		} else {
			dataRoot, err = canonicalPath(dataRoot)
			if err != nil {
				return "", err
			}
		}
		root, err = canonicalPath(filepath.Join(dataRoot, programName, sharedStateDirectoryName))
	}
	if err != nil {
		return "", err
	}
	projectContains, err := pathIsWithin(root, canonicalProject)
	if err != nil {
		return "", err
	}
	rootContains, err := pathIsWithin(canonicalProject, root)
	if err != nil {
		return "", err
	}
	if projectContains || rootContains {
		return "", ljaError("shared state root overlaps project: %s; use %s", root, projectStateFlag)
	}
	if info, statErr := os.Stat(root); statErr == nil {
		if !info.IsDir() {
			return "", ljaError("state root is not a directory: %s", root)
		}
	} else if !os.IsNotExist(statErr) {
		return "", ljaError("cannot inspect state root %s: %w", root, statErr)
	}
	return root, nil
}

func ResolveStateRoot(project string, stateDirectory *string, projectState bool, environment map[string]string) (string, error) {
	return resolveStateRoot(project, stateDirectory, projectState, environment)
}

func agentStateEnvironment(stateRoot string, agentName string) (map[string]string, error) {
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return nil, err
	}
	if _, err := AgentSpecByName(agentName); err != nil {
		return nil, err
	}
	if agentName == codexAgentName {
		return map[string]string{codeXHomeEnvironment: filepath.Join(canonicalState, codexStateDirectoryName)}, nil
	}
	if agentName == claudeAgentName {
		return map[string]string{claudeConfigEnvironment: filepath.Join(canonicalState, claudeStateDirectoryName)}, nil
	}
	stateDirectory := filepath.Join(canonicalState, openCodeStateDirectoryName)
	environment := map[string]string{openCodeConfigEnvironment: stateDirectory}
	for _, entry := range []struct {
		name      string
		directory string
	}{
		{name: xdgConfigHomeEnv, directory: "config"},
		{name: xdgDataHomeEnv, directory: "data"},
		{name: xdgStateHomeEnv, directory: "state"},
		{name: xdgCacheHomeEnv, directory: "cache"},
	} {
		environment[entry.name] = filepath.Join(stateDirectory, entry.directory)
	}
	return environment, nil
}

func AgentStateEnvironment(stateRoot string, agentName string) (map[string]string, error) {
	return agentStateEnvironment(stateRoot, agentName)
}

func agentStateDirectories(stateRoot string, agentName string) ([]string, error) {
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return nil, err
	}
	if _, err := AgentSpecByName(agentName); err != nil {
		return nil, err
	}
	if agentName == codexAgentName {
		return []string{filepath.Join(canonicalState, codexStateDirectoryName)}, nil
	}
	if agentName == claudeAgentName {
		return []string{filepath.Join(canonicalState, claudeStateDirectoryName)}, nil
	}
	stateDirectory := filepath.Join(canonicalState, openCodeStateDirectoryName)
	directories := []string{stateDirectory}
	for _, name := range []string{"config", "data", "state", "cache"} {
		directories = append(directories, filepath.Join(stateDirectory, name))
	}
	return directories, nil
}

func EnsureAgentStateDirectories(stateRoot string, agentName string) ([]string, error) {
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return nil, err
	}
	directories, err := agentStateDirectories(canonicalState, agentName)
	if err != nil {
		return nil, err
	}
	for _, directory := range directories {
		resolvedDirectory, resolveErr := canonicalPath(directory)
		if resolveErr != nil {
			return nil, resolveErr
		}
		within, withinErr := pathIsWithin(resolvedDirectory, canonicalState)
		if withinErr != nil {
			return nil, withinErr
		}
		if !within {
			return nil, ljaError("agent state directory would leave state root: %s", directory)
		}
		if err := ensurePrivateDirectory(directory); err != nil {
			return nil, err
		}
		info, statErr := os.Stat(directory)
		if statErr != nil {
			return nil, ljaError("cannot inspect agent state directory %s: %w", directory, statErr)
		}
		if !info.IsDir() {
			return nil, ljaError("agent state path is not a directory: %s", directory)
		}
		resolvedDirectory, err = canonicalPath(directory)
		if err != nil {
			return nil, err
		}
		within, err = pathIsWithin(resolvedDirectory, canonicalState)
		if err != nil {
			return nil, err
		}
		if !within {
			return nil, ljaError("agent state directory would leave state root: %s", directory)
		}
	}
	return directories, nil
}

func agentInstructionPaths(stateRoot string, agentName string, hostEnvironment map[string]string) (string, string, error) {
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return "", "", err
	}
	if _, err := AgentSpecByName(agentName); err != nil {
		return "", "", err
	}
	if hostEnvironment == nil {
		hostEnvironment = environmentMap()
	}
	home, err := homeDirectory()
	if err != nil {
		return "", "", err
	}
	if agentName == codexAgentName {
		sourceDirectory, resolveErr := configuredHostDirectory(hostEnvironment, codeXHomeEnvironment, filepath.Join(home, codexStateDirectoryName))
		if resolveErr != nil {
			return "", "", resolveErr
		}
		return filepath.Join(sourceDirectory, agentInstructionFileName), filepath.Join(canonicalState, codexStateDirectoryName, agentInstructionFileName), nil
	}
	if agentName == claudeAgentName {
		sourceDirectory, resolveErr := configuredHostDirectory(hostEnvironment, claudeConfigEnvironment, filepath.Join(home, claudeStateDirectoryName))
		if resolveErr != nil {
			return "", "", resolveErr
		}
		return filepath.Join(sourceDirectory, claudeInstructionFileName), filepath.Join(canonicalState, claudeStateDirectoryName, claudeInstructionFileName), nil
	}
	configHome := filepath.Join(home, ".config")
	if _, present := hostEnvironment[xdgConfigHomeEnv]; present {
		configHome, err = configuredHostDirectory(hostEnvironment, xdgConfigHomeEnv, configHome)
		if err != nil {
			return "", "", err
		}
	} else {
		configHome, err = canonicalPath(configHome)
		if err != nil {
			return "", "", err
		}
	}
	sourceDirectory, err := configuredHostDirectory(hostEnvironment, openCodeConfigEnvironment, filepath.Join(configHome, openCodeDirectoryName))
	if err != nil {
		return "", "", err
	}
	return filepath.Join(sourceDirectory, agentInstructionFileName), filepath.Join(canonicalState, openCodeStateDirectoryName, "config", openCodeDirectoryName, agentInstructionFileName), nil
}

func AgentInstructionPaths(stateRoot string, agentName string, hostEnvironment map[string]string) (string, string, error) {
	return agentInstructionPaths(stateRoot, agentName, hostEnvironment)
}

func validateInstructionDestination(stateRoot string, destination string) error {
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return err
	}
	resolvedDestination, err := canonicalPath(destination)
	if err != nil {
		return err
	}
	within, err := pathIsWithin(resolvedDestination, canonicalState)
	if err != nil {
		return err
	}
	if !within {
		return ljaError("instruction destination leaves state root: %s", destination)
	}
	info, statErr := os.Lstat(destination)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return ljaError("cannot inspect instruction destination %s: %w", destination, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ljaError("instruction destination is a symbolic link: %s", destination)
	}
	if !info.Mode().IsRegular() {
		return ljaError("instruction destination is not a regular file: %s", destination)
	}
	return nil
}

func RefreshAgentInstructions(stateRoot string, agentName string, hostEnvironment map[string]string) (bool, error) {
	canonicalState, err := canonicalProjectPath(stateRoot)
	if err != nil {
		return false, err
	}
	source, destination, err := agentInstructionPaths(canonicalState, agentName, hostEnvironment)
	if err != nil {
		return false, err
	}
	if err := validateInstructionDestination(canonicalState, destination); err != nil {
		return false, err
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Debug("instruction source missing", "agent", agentName, "source", source)
			return false, nil
		}
		return false, ljaError("cannot inspect instruction source %s: %w", source, err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return false, ljaError("instruction source is not a regular file: %s", source)
	}
	parent := filepath.Dir(destination)
	if err := ensurePrivateDirectory(parent); err != nil {
		return false, err
	}
	if err := validateInstructionDestination(canonicalState, destination); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(parent, "."+filepath.Base(destination)+".*")
	if err != nil {
		return false, ljaError("cannot replace instruction destination %s: %w", destination, err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			if removeErr := os.Remove(temporaryPath); removeErr != nil && !os.IsNotExist(removeErr) {
				slog.Warn("cannot remove temporary instruction file", "path", temporaryPath, "error", removeErr)
			}
		}
	}()
	sourceFile, err := os.Open(source)
	if err != nil {
		closeErr := temporary.Close()
		if closeErr != nil {
			return false, errors.Join(ljaError("cannot read instruction source %s: %w", source, err), closeErr)
		}
		return false, ljaError("cannot read instruction source %s: %w", source, err)
	}
	_, copyErr := io.Copy(temporary, sourceFile)
	closeSourceErr := sourceFile.Close()
	syncErr := temporary.Sync()
	closeTemporaryErr := temporary.Close()
	if copyErr != nil || closeSourceErr != nil || syncErr != nil || closeTemporaryErr != nil {
		return false, ljaError("cannot replace instruction destination %s: %w", destination, firstError(copyErr, closeSourceErr, syncErr, closeTemporaryErr))
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return false, ljaError("cannot replace instruction destination %s: %w", destination, err)
	}
	keepTemporary = false
	slog.Debug("refreshed instructions", "agent", agentName, "source", source, "destination", destination)
	return true, nil
}

func firstError(errorsToCheck ...error) error {
	for _, err := range errorsToCheck {
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("unknown error")
}
