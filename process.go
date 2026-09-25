package lja

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	processPipeWaitTimeout   = time.Second
	limaCtlCommand           = "limactl"
	limaNoninteractiveFlag   = "--tty=false"
	limaWorkdirFlag          = "--workdir"
	shellCommand             = "sh"
	shellCommandFlag         = "-c"
	shellStrictFlag          = "-eu"
	shellStrictScript        = "set -eu"
	guestConnectionProbe     = "true"
	guestCommandProbe        = "command"
	guestCommandProbeFlag    = "-v"
	temporaryPathAssignment  = "temporary_path="
	guestPathBootstrapScript = `set -eu
PATH="${PATH-}"
export PATH

prepend_path() {
    if [ -z "$1" ]; then
        return 0
    fi
    case ":$PATH:" in
        *":$1:"*) ;;
        *)
            if [ -n "$PATH" ]; then
                PATH="$1:$PATH"
            else
                PATH="$1"
            fi
            ;;
    esac
}

if [ -n "${HOME-}" ]; then
    prepend_path "$HOME/.local/bin"
    prepend_path "$HOME/go/bin"
fi
prepend_path "${PIPX_BIN_DIR-}"
prepend_path "${GOBIN-}"
if [ -n "${GOPATH-}" ]; then
    guest_gopath="${GOPATH%%:*}"
    prepend_path "$guest_gopath/bin"
fi
if command -v go >/dev/null 2>&1; then
    guest_gobin="$(go env GOBIN)" || exit $?
    if [ -n "$guest_gobin" ]; then
        prepend_path "$guest_gobin"
    else
        guest_gopath="$(go env GOPATH)" || exit $?
        guest_gopath="${guest_gopath%%:*}"
        prepend_path "$guest_gopath/bin"
    fi
fi
export PATH
if [ "${1-}" = "` + guestCommandProbe + `" ] && [ "${2-}" = "` + guestCommandProbeFlag + `" ]; then
    if "$@"; then
        exit 0
    fi
    exit 1
else
    exec "$@"
fi
`
)

type ProcessResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type GuestOptions struct {
	Command     string
	Environment map[string]string
}

type processOptions struct {
	context          context.Context
	command          string
	workingDirectory string
	captureOutput    bool
	check            bool
	inputData        []byte
	hasInput         bool
}

type guestExecutionOptions struct {
	processOptions processOptions
	environment    map[string]string
}

func guestExecution(options processOptions, environment map[string]string) guestExecutionOptions {
	return guestExecutionOptions{processOptions: options, environment: environment}
}

func defaultProcessOptions(command string, contexts ...context.Context) processOptions {
	options := processOptions{command: command, check: true}
	if len(contexts) > 0 {
		options.context = contexts[0]
	}
	return options
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
	ctx := options.context
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, command, arguments...)
	if options.workingDirectory != "" {
		cmd.Dir = options.workingDirectory
	}
	if options.context != nil {
		cmd.WaitDelay = processPipeWaitTimeout
	}
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
	} else {
		cmd.Stdin = os.Stdin
	}
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return ProcessResult{}, fmt.Errorf("lima operation canceled: %w", context.Cause(ctx))
	}
	result := ProcessResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0}
	if runErr != nil {
		if exitError, ok := errors.AsType[*exec.ExitError](runErr); ok {
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

func guestArgumentsWithUserPath(arguments []string) []string {
	wrapped := []string{shellCommand, shellCommandFlag, guestPathBootstrapScript, programName}
	return append(wrapped, arguments...)
}

func guestArgumentsWithPath(arguments []string, path string) []string {
	if path == "" {
		return guestArgumentsWithUserPath(arguments)
	}
	pathScript := "set -eu\nPATH=" + shellQuote(path) + ":${PATH-}\nexport PATH\nexec \"$@\"\n"
	wrapped := guestArgumentsWithUserPath([]string{shellCommand, shellCommandFlag, pathScript, programName})
	return append(wrapped, arguments...)
}

func runGuest(project, vmName string, arguments []string, execution guestExecutionOptions) (ProcessResult, error) {
	if len(arguments) == 0 {
		return ProcessResult{}, ljaError("cannot run an empty guest command")
	}
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return ProcessResult{}, err
	}
	environmentArguments := make([]string, 0, len(execution.environment))
	names := make([]string, 0, len(execution.environment))
	for name := range execution.environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !environmentNamePattern.MatchString(name) {
			return ProcessResult{}, ljaError("invalid guest environment variable name: %s", name)
		}
		value := execution.environment[name]
		if strings.IndexByte(value, 0) >= 0 {
			return ProcessResult{}, ljaError("guest environment value for %s contains a NUL character", name)
		}
		environmentArguments = append(environmentArguments, name+"="+value)
	}
	limaArguments := make([]string, 0, 6+len(environmentArguments)+len(arguments))
	limaArguments = append(limaArguments, limaShellOperation)
	if execution.processOptions.hasInput {
		limaArguments = append(limaArguments, limaNoninteractiveFlag)
	}
	limaArguments = append(limaArguments, limaWorkdirFlag, canonicalProject, vmName)
	limaArguments = append(limaArguments, environmentArguments...)
	limaArguments = append(limaArguments, guestArgumentsWithUserPath(arguments)...)
	return runLima(limaArguments, execution.processOptions)
}

func RunGuest(project, vmName string, arguments []string, guestOptions GuestOptions) (ProcessResult, error) {
	options := defaultProcessOptions(guestOptions.Command)
	return runGuest(project, vmName, arguments, guestExecution(options, guestOptions.Environment))
}

func guestConnectionError(vmName string, result ProcessResult) error {
	detail := processOutput(result)
	if detail != "" {
		return ljaError("cannot connect to guest VM %s: %s", vmName, detail)
	}
	return ljaError("cannot connect to guest VM %s: exit status %d", vmName, result.ExitCode)
}

func verifyGuestConnection(project, vmName string, options processOptions) error {
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{guestConnectionProbe}, guestExecution(options, nil))
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return guestConnectionError(vmName, result)
	}
	return nil
}

func guestExecutablePath(project, vmName, executable string, options processOptions) (string, error) {
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{guestCommandProbe, guestCommandProbeFlag, executable}, guestExecution(options, nil))
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
	if err := verifyGuestConnection(project, vmName, options); err != nil {
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

func guestExecutableAvailable(project, vmName, executable string, options processOptions) (bool, error) {
	path, err := guestExecutablePath(project, vmName, executable, options)
	return path != "", err
}
