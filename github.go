package lja

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const githubTokenTimeout = 30 * time.Second

const ghCommand = "gh"

type githubWorkflow struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	token    string
	resolved bool
	err      error
}

func (workflow *githubWorkflow) resolve(config GitHubConfig, environment map[string]string, hostHome string) error {
	if workflow.resolved {
		return workflow.err
	}
	workflow.token, workflow.err = resolveGitHubToken(workflow.ctx, config, environment, hostHome)
	workflow.resolved = true
	return workflow.err
}

func (workflow *githubWorkflow) close() {
	if workflow == nil {
		return
	}
	workflow.token = ""
	if workflow.cancel != nil {
		workflow.cancel(nil)
	}
}

func (workflow *githubWorkflow) context() context.Context {
	if workflow == nil {
		return nil
	}
	return workflow.ctx
}

func (workflow *githubWorkflow) environment(environment map[string]string) (map[string]string, error) {
	if workflow == nil {
		return environment, nil
	}
	if workflow.ctx != nil {
		if err := workflow.ctx.Err(); err != nil {
			return nil, context.Cause(workflow.ctx)
		}
	}
	result, err := githubGitEnvironment(environment)
	if err != nil {
		return nil, err
	}
	result[githubTokenEnvironment] = workflow.token
	return result, nil
}

func (options *WorkflowOptions) ownGitHubWorkflow() (func(*error), error) {
	config := developmentConfigOrDefault(options.Development)
	if !config.GitHub.Enabled {
		return func(*error) {}, nil
	}
	if err := config.validateGitHubEnvironment(options.Environment); err != nil {
		return nil, err
	}
	if options.github != nil {
		return func(*error) {}, nil
	}
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancelCause(signalContext)
	workflow := &githubWorkflow{ctx: ctx, cancel: cancel}
	hostEnvironment := options.hostEnvironment
	if hostEnvironment == nil {
		hostEnvironment = environmentMap()
	}
	hostHome := options.hostHome
	if hostHome == "" {
		var err error
		hostHome, err = homeDirectory()
		if err != nil {
			cancel(err)
			stop()
			return nil, err
		}
	}
	if err := workflow.resolve(config.GitHub, hostEnvironment, hostHome); err != nil {
		workflow.close()
		stop()
		return nil, err
	}
	options.github = workflow
	return func(_ *error) {
		workflow.close()
		stop()
	}, nil
}

func validateGitHubToken(value, source string, allowTrailingLineEnding bool) (string, error) {
	if allowTrailingLineEnding {
		switch {
		case strings.HasSuffix(value, "\r\n"):
			value = strings.TrimSuffix(value, "\r\n")
		case strings.HasSuffix(value, "\n"):
			value = strings.TrimSuffix(value, "\n")
		}
	}
	if value == "" {
		return "", ljaError("GitHub token from %s is invalid: expected one nonempty line", source)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return "", ljaError("GitHub token from %s is invalid: expected one nonempty line without control characters", source)
		}
	}
	return value, nil
}

func runGitHubTokenCommand(ctx context.Context, command []string, hostHome string) (string, error) {
	if len(command) == 0 {
		return "", ljaError("GitHub token command is empty")
	}
	for index, argument := range command {
		if argument == "" {
			return "", ljaError("GitHub token command argument %d is empty", index)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandContext, cancel := context.WithTimeout(ctx, githubTokenTimeout)
	defer cancel()
	commandPath := command[0]
	process := exec.CommandContext(commandContext, commandPath, command[1:]...)
	process.Dir = hostHome
	process.Stdin = bytes.NewReader(nil)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	runErr := process.Run()
	if ctx.Err() != nil {
		cause := context.Cause(commandContext)
		if cause == nil {
			cause = ctx.Err()
		}
		return "", ljaError("GitHub token command %q was canceled: %v", commandPath, cause)
	}
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		return "", ljaError("GitHub token command %q timed out after %s", commandPath, githubTokenTimeout)
	}
	if runErr != nil {
		if exitError, ok := errors.AsType[*exec.ExitError](runErr); ok {
			return "", ljaError("GitHub token command %q failed with exit status %d", commandPath, exitError.ExitCode())
		}
		return "", ljaError("cannot execute GitHub token command %q: %w", commandPath, runErr)
	}
	return validateGitHubToken(stdout.String(), fmt.Sprintf("command %q", commandPath), true)
}

func resolveGitHubToken(ctx context.Context, config GitHubConfig, environment map[string]string, hostHome string) (string, error) {
	if !config.Enabled {
		return "", nil
	}
	for _, name := range []string{githubTokenEnvironment, githubFallbackTokenEnvironment} {
		value, present := environment[name]
		if !present || value == "" {
			continue
		}
		return validateGitHubToken(value, name, false)
	}
	if len(config.TokenCommand) == 0 {
		return "", ljaError("GitHub authentication is enabled but no token was provided by %s, %s, or token_command", githubTokenEnvironment, githubFallbackTokenEnvironment)
	}
	if hostHome == "" {
		var err error
		hostHome, err = homeDirectory()
		if err != nil {
			return "", err
		}
	}
	return runGitHubTokenCommand(ctx, config.TokenCommand, hostHome)
}

func ResolveGitHubToken(ctx context.Context, config GitHubConfig, environment map[string]string, hostHome string) (string, error) {
	return resolveGitHubToken(ctx, config, environment, hostHome)
}
