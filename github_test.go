package lja

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func writeGitHubExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token-command")
	content := "#!/bin/sh\nset -eu\n" + body + "\n"
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o700))
	return path
}

func TestGitHubTokenResolutionPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		environment    map[string]string
		disabled       bool
		command        bool
		wantToken      string
		wantError      string
		wantInvocation bool
	}{
		{name: "primary environment", environment: map[string]string{githubTokenEnvironment: "primary"}, command: true, wantToken: "primary"},
		{name: "fallback environment", environment: map[string]string{githubFallbackTokenEnvironment: "fallback"}, command: true, wantToken: "fallback"},
		{name: "empty primary falls back", environment: map[string]string{githubTokenEnvironment: "", githubFallbackTokenEnvironment: "fallback"}, command: true, wantToken: "fallback"},
		{name: "malformed primary does not fall back", environment: map[string]string{githubTokenEnvironment: "bad\nvalue", githubFallbackTokenEnvironment: "fallback"}, command: true, wantError: githubTokenEnvironment},
		{name: "malformed fallback does not use command", environment: map[string]string{githubFallbackTokenEnvironment: "bad\nvalue"}, command: true, wantError: githubFallbackTokenEnvironment},
		{name: "missing credentials", wantError: "no token was provided"},
		{name: "disabled", environment: map[string]string{githubTokenEnvironment: "manual"}, disabled: true, command: true, wantToken: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "invoked")
			command := writeGitHubExecutable(t, "printf '%s' invoked > "+shellQuote(marker)+"\nprintf '%s\\n' command")
			config := GitHubConfig{Enabled: !test.disabled}
			if test.command {
				config.TokenCommand = []string{command}
			}
			token, err := resolveGitHubToken(context.Background(), config, test.environment, t.TempDir())
			if test.wantError == "" {
				assert.NilError(t, err)
				assert.Equal(t, token, test.wantToken)
			} else {
				assert.ErrorContains(t, err, test.wantError)
			}
			_, statErr := os.Stat(marker)
			invoked := statErr == nil
			assert.Equal(t, invoked, test.wantInvocation)
		})
	}
}

func TestGitHubTokenCommandOutputValidation(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantToken string
		wantError string
	}{
		{name: "LF", body: "printf 'token\\n'", wantToken: "token"},
		{name: "CRLF", body: "printf 'token\\r\\n'", wantToken: "token"},
		{name: "no newline", body: "printf token", wantToken: "token"},
		{name: "empty", body: "printf '\\n'", wantError: "invalid"},
		{name: "multiple lines", body: "printf 'token\\nsecond\\n'", wantError: "invalid"},
		{name: "control character", body: "printf 'token\\tvalue'", wantError: "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := writeGitHubExecutable(t, test.body)
			token, err := resolveGitHubToken(context.Background(), GitHubConfig{Enabled: true, TokenCommand: []string{command}}, nil, t.TempDir())
			if test.wantError == "" {
				assert.NilError(t, err)
				assert.Equal(t, token, test.wantToken)
				return
			}
			assert.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestGitHubTokenCommandUsesArgumentsHomeAndClosedStdin(t *testing.T) {
	root := t.TempDir()
	argumentsPath := filepath.Join(root, "arguments")
	command := writeGitHubExecutable(t, "printf '%s\\n' \"$PWD\" > "+shellQuote(filepath.Join(root, "working-directory"))+"\nprintf '%s\\n' \"$#\" > "+shellQuote(argumentsPath)+"\nfor argument in \"$@\"; do printf '<%s>\\n' \"$argument\" >> "+shellQuote(argumentsPath)+"; done\nif IFS= read -r input; then exit 23; fi\nprintf 'token\\n'")
	unsafeArgument := "$(touch " + filepath.Join(root, "expanded") + ")"
	token, err := resolveGitHubToken(context.Background(), GitHubConfig{Enabled: true, TokenCommand: []string{command, unsafeArgument, "with spaces"}}, nil, root)
	assert.NilError(t, err)
	assert.Equal(t, token, "token")
	workingDirectory, err := os.ReadFile(filepath.Join(root, "working-directory"))
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(workingDirectory)), root)
	arguments, err := os.ReadFile(argumentsPath)
	assert.NilError(t, err)
	assert.Equal(t, string(arguments), "2\n<"+unsafeArgument+">\n<with spaces>\n")
	_, err = os.Stat(filepath.Join(root, "expanded"))
	assert.Assert(t, os.IsNotExist(err))
}

func TestGitHubTokenCommandDiagnosticsAndCancellation(t *testing.T) {
	failureCommand := writeGitHubExecutable(t, "printf 'secret stdout'\nprintf 'secret stderr' >&2\nexit 7")
	_, err := resolveGitHubToken(context.Background(), GitHubConfig{Enabled: true, TokenCommand: []string{failureCommand, "secret argument"}}, nil, t.TempDir())
	assert.ErrorContains(t, err, "exit status 7")
	assert.Assert(t, !strings.Contains(err.Error(), "secret stdout"))
	assert.Assert(t, !strings.Contains(err.Error(), "secret stderr"))
	assert.Assert(t, !strings.Contains(err.Error(), "secret argument"))

	contextWithTimeout, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	slowCommand := writeGitHubExecutable(t, "sleep 1\nprintf token")
	_, err = resolveGitHubToken(contextWithTimeout, GitHubConfig{Enabled: true, TokenCommand: []string{slowCommand}}, nil, t.TempDir())
	assert.ErrorContains(t, err, "canceled")
	assert.Equal(t, githubTokenTimeout, 30*time.Second)
}

func TestGitHubWorkflowCachesResolution(t *testing.T) {
	root := t.TempDir()
	countPath := filepath.Join(root, "count")
	command := writeGitHubExecutable(t, "count=0\nif [ -f "+shellQuote(countPath)+" ]; then count=$(cat "+shellQuote(countPath)+"); fi\ncount=$((count + 1))\nprintf '%s' \"$count\" > "+shellQuote(countPath)+"\nprintf 'token\\n'")
	config := GitHubConfig{Enabled: true, TokenCommand: []string{command}}
	workflow := &githubWorkflow{ctx: context.Background()}
	assert.NilError(t, workflow.resolve(config, nil, root))
	assert.NilError(t, workflow.resolve(config, nil, root))
	count, err := os.ReadFile(countPath)
	assert.NilError(t, err)
	assert.Equal(t, string(count), "1")
	workflow.close()

	secondWorkflow := &githubWorkflow{ctx: context.Background()}
	assert.NilError(t, secondWorkflow.resolve(config, nil, root))
	count, err = os.ReadFile(countPath)
	assert.NilError(t, err)
	assert.Equal(t, string(count), "2")
	secondWorkflow.close()
}

func TestGitHubTokenResolutionIsReusedDuringRecreation(t *testing.T) {
	project, _, options := vmFixture(t, limaStatusRunning)
	root := t.TempDir()
	countPath := filepath.Join(root, "count")
	command := writeGitHubExecutable(t, "count=0\nif [ -f "+shellQuote(countPath)+" ]; then count=$(cat "+shellQuote(countPath)+"); fi\ncount=$((count + 1))\nprintf '%s' \"$count\" > "+shellQuote(countPath)+"\nprintf 'token\\n'")
	config := *options.Development
	config.GitHub = GitHubConfig{Enabled: true, TokenCommand: []string{command}}
	options.Development = &config
	options.Recreate = true
	options.hostEnvironment = map[string]string{}
	options.hostHome = root
	_, err := CreateVM(project, options)
	assert.NilError(t, err)
	count, err := os.ReadFile(countPath)
	assert.NilError(t, err)
	assert.Equal(t, string(count), "1")
}

func TestGitHubTokenResolutionSkipsExistingNoopCreate(t *testing.T) {
	project, _, options := vmFixture(t, limaStatusRunning)
	root := t.TempDir()
	marker := filepath.Join(root, "invoked")
	command := writeGitHubExecutable(t, "printf '%s' invoked > "+shellQuote(marker)+"\nprintf 'token\\n'")
	config := *options.Development
	config.GitHub = GitHubConfig{Enabled: true, TokenCommand: []string{command}}
	options.Development = &config
	options.hostEnvironment = map[string]string{}
	options.hostHome = root
	_, err := CreateVM(project, options)
	assert.NilError(t, err)
	_, statErr := os.Stat(marker)
	assert.Assert(t, os.IsNotExist(statErr))
}

func operationHasEnvironment(operation []string, name string, value string) bool {
	want := name + "=" + value
	for _, argument := range operation {
		if argument == want {
			return true
		}
	}
	return false
}

func TestGitHubPreparationInstallsDependenciesAndForwardsToken(t *testing.T) {
	project, _, options := vmFixture(t, limaStatusRunning)
	config := DevelopmentConfig{
		GitHub:        GitHubConfig{Enabled: true},
		Packages:      []string{},
		CopyGitConfig: false,
		Setup:         []SetupCommand{{Command: testSetupCommand}},
	}
	options.Development = &config
	options.Environment = map[string]string{
		gitConfigCountEnv:             "1",
		gitConfigKeyEnvPrefix + "0":   "user.name",
		gitConfigValueEnvPrefix + "0": "Example",
	}
	options.github = &githubWorkflow{ctx: context.Background(), token: "test-token"}

	_, err := OpenShell(project, []string{"printf", "shell"}, project, options)
	assert.NilError(t, err)
	database, err := readVMDatabase()
	assert.NilError(t, err)
	packageIndex := -1
	setupIndex := -1
	verifiedGit := false
	verifiedGH := false
	for index, operation := range database.Operations {
		if len(operation) < 5 {
			continue
		}
		guestArguments := unwrapGuestPathArguments(operation[4:])
		if len(guestArguments) == 3 && guestArguments[0] == guestCommandProbe && guestArguments[1] == guestCommandProbeFlag {
			verifiedGit = verifiedGit || guestArguments[2] == gitCommand
			verifiedGH = verifiedGH || guestArguments[2] == ghCommand
		}
		if len(operation) >= 5 && isPackageInstallationCommand(operation[4:]) {
			packageIndex = index
			assert.Assert(t, !operationHasEnvironment(operation, githubTokenEnvironment, "test-token"))
			assert.Assert(t, strings.Contains(guestArguments[3], "'git' 'gh'"))
		}
		if len(operation) > 0 && operation[len(operation)-1] == testSetupCommand {
			setupIndex = index
			assert.Assert(t, operationHasEnvironment(operation, githubTokenEnvironment, "test-token"))
			assert.Assert(t, operationHasEnvironment(operation, gitConfigCountEnv, "5"))
			assert.Assert(t, operationHasEnvironment(operation, gitConfigKeyEnvPrefix+"0", "user.name"))
		}
	}
	assert.Assert(t, packageIndex >= 0)
	assert.Assert(t, setupIndex >= 0)
	assert.Assert(t, packageIndex < setupIndex)
	assert.Assert(t, verifiedGit)
	assert.Assert(t, verifiedGH)
	assert.Assert(t, operationHasEnvironment(database.Operations[len(database.Operations)-1], githubTokenEnvironment, "test-token"))
	assert.Assert(t, operationHasEnvironment(database.Operations[len(database.Operations)-1], gitConfigCountEnv, "5"))
}

func TestGitHubAgentCommandForwardsToken(t *testing.T) {
	project, _, options := vmFixture(t, limaStatusRunning)
	config := DevelopmentConfig{
		GitHub:        GitHubConfig{Enabled: true},
		Packages:      []string{},
		CopyGitConfig: false,
	}
	options.Development = &config
	options.github = &githubWorkflow{ctx: context.Background(), token: "agent-token"}

	_, err := RunAgent(project, codexAgentName, []string{"--version"}, nil, project, options)
	assert.NilError(t, err)
	database, err := readVMDatabase()
	assert.NilError(t, err)
	lastOperation := database.Operations[len(database.Operations)-1]
	assert.Assert(t, operationHasEnvironment(lastOperation, githubTokenEnvironment, "agent-token"))
}

func TestGitHubConcurrentInvocationsKeepTokensSeparate(t *testing.T) {
	tokens := []string{"first-token", "second-token"}
	results := make(chan string, len(tokens))
	var waitGroup sync.WaitGroup
	for _, token := range tokens {
		token := token
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			workflow := &githubWorkflow{ctx: context.Background(), token: token}
			environment, err := workflow.environment(map[string]string{"INVOCATION": token})
			if err != nil {
				results <- err.Error()
				return
			}
			if environment[githubTokenEnvironment] != token || environment["INVOCATION"] != token {
				results <- "token environment crossed invocation boundary"
				return
			}
			results <- token
		}()
	}
	waitGroup.Wait()
	close(results)
	seen := make(map[string]bool, len(tokens))
	for result := range results {
		assert.Assert(t, result == tokens[0] || result == tokens[1])
		seen[result] = true
	}
	assert.Equal(t, len(seen), len(tokens))
}

func TestGitHubAgentWrapperInheritsToken(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "token")
	agentPath := filepath.Join(root, "agent")
	agentScript := "#!/bin/sh\nprintf '%s' \"$GH_TOKEN\" > " + shellQuote(marker) + "\n"
	assert.NilError(t, os.WriteFile(agentPath, []byte(agentScript), 0o700))
	content, err := AgentWrapperContent(root, codexAgentName, agentPath)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(content, "first-token"))
	wrapperPath := filepath.Join(root, "wrapper")
	assert.NilError(t, os.WriteFile(wrapperPath, []byte(content), 0o700))
	command := exec.Command(wrapperPath)
	command.Env = append(os.Environ(), githubTokenEnvironment+"=nested-token")
	assert.NilError(t, command.Run())
	value, err := os.ReadFile(marker)
	assert.NilError(t, err)
	assert.Equal(t, string(value), "nested-token")
}
