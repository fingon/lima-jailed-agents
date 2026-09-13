package lja

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	limaCtlCommand         = "limactl"
	limaNoninteractiveFlag = "--tty=false"
	limaWorkdirFlag        = "--workdir"
	shellCommand           = "sh"
	shellCommandFlag       = "-c"
	guestConnectionProbe   = "true"
	guestCommandProbe      = "command"
	guestCommandProbeFlag  = "-v"
)

type ProcessResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type processOptions struct {
	command       string
	captureOutput bool
	check         bool
	inputData     []byte
	hasInput      bool
}

func defaultProcessOptions(command string) processOptions {
	return processOptions{command: command, check: true}
}

func runLima(arguments []string, options processOptions) (ProcessResult, error) {
	if len(arguments) == 0 {
		return ProcessResult{}, ljaError("cannot run an empty Lima operation")
	}
	command := options.command
	if command == "" {
		command = limaCtlCommand
	}
	slog.Debug("running Lima operation", "operation", arguments[0])
	cmd := exec.Command(command, arguments...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if options.captureOutput {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if options.hasInput {
		cmd.Stdin = bytes.NewReader(options.inputData)
	}
	runErr := cmd.Run()
	result := ProcessResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0}
	if runErr != nil {
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			result.ExitCode = exitError.ExitCode()
		} else {
			return ProcessResult{}, ljaError("cannot execute %s: %w", command, runErr)
		}
	}
	if options.check && result.ExitCode != 0 {
		detail := processOutput(result)
		if detail != "" {
			return result, ljaError("Lima operation %s failed with exit status %d: %s", arguments[0], result.ExitCode, detail)
		}
		return result, ljaError("Lima operation %s failed with exit status %d", arguments[0], result.ExitCode)
	}
	return result, nil
}

func RunLima(arguments []string, command string) (ProcessResult, error) {
	options := defaultProcessOptions(command)
	return runLima(arguments, options)
}

func processOutput(result ProcessResult) string {
	parts := make([]string, 0, 2)
	for _, output := range [][]byte{result.Stderr, result.Stdout} {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n")
}

func runGuest(project string, vmName string, arguments []string, options processOptions, environment map[string]string) (ProcessResult, error) {
	if len(arguments) == 0 {
		return ProcessResult{}, ljaError("cannot run an empty guest command")
	}
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return ProcessResult{}, err
	}
	environmentArguments := make([]string, 0, len(environment))
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !environmentNamePattern.MatchString(name) {
			return ProcessResult{}, ljaError("invalid guest environment variable name: %s", name)
		}
		value := environment[name]
		if strings.IndexByte(value, 0) >= 0 {
			return ProcessResult{}, ljaError("guest environment value for %s contains a NUL character", name)
		}
		environmentArguments = append(environmentArguments, name+"="+value)
	}
	limaArguments := make([]string, 0, 6+len(environmentArguments)+len(arguments))
	limaArguments = append(limaArguments, "shell")
	if options.hasInput {
		limaArguments = append(limaArguments, limaNoninteractiveFlag)
	}
	limaArguments = append(limaArguments, limaWorkdirFlag, canonicalProject, vmName)
	limaArguments = append(limaArguments, environmentArguments...)
	limaArguments = append(limaArguments, arguments...)
	return runLima(limaArguments, options)
}

func RunGuest(project string, vmName string, arguments []string, command string, environment map[string]string) (ProcessResult, error) {
	options := defaultProcessOptions(command)
	return runGuest(project, vmName, arguments, options, environment)
}

func guestConnectionError(vmName string, result ProcessResult) error {
	detail := processOutput(result)
	if detail != "" {
		return ljaError("cannot connect to guest VM %s: %s", vmName, detail)
	}
	return ljaError("cannot connect to guest VM %s: exit status %d", vmName, result.ExitCode)
}

func verifyGuestConnection(project string, vmName string, command string) error {
	options := defaultProcessOptions(command)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{guestConnectionProbe}, options, nil)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return guestConnectionError(vmName, result)
	}
	return nil
}

func guestExecutablePath(project string, vmName string, executable string, command string) (string, error) {
	options := defaultProcessOptions(command)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{guestCommandProbe, guestCommandProbeFlag, executable}, options, nil)
	if err != nil {
		return "", err
	}
	if result.ExitCode == 0 {
		executablePath := strings.TrimSpace(string(result.Stdout))
		if executablePath == "" {
			return "", ljaError("guest command probe for %s returned no executable path", executable)
		}
		if strings.ContainsAny(executablePath, "\r\n") {
			return "", ljaError("guest command probe for %s returned multiple paths", executable)
		}
		if !strings.HasPrefix(executablePath, "/") {
			return "", ljaError("guest command probe for %s returned a non-absolute path", executable)
		}
		return executablePath, nil
	}
	if err := verifyGuestConnection(project, vmName, command); err != nil {
		return "", err
	}
	if result.ExitCode == 1 {
		return "", nil
	}
	detail := processOutput(result)
	if detail != "" {
		return "", ljaError("guest command probe for %s failed with exit status %d: %s", executable, result.ExitCode, detail)
	}
	return "", ljaError("guest command probe for %s failed with exit status %d", executable, result.ExitCode)
}

func guestExecutableAvailable(project string, vmName string, executable string, command string) (bool, error) {
	path, err := guestExecutablePath(project, vmName, executable, command)
	return path != "", err
}
