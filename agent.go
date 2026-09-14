package lja

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
)

const (
	codexAgentName                   = "codex"
	claudeAgentName                  = "claude"
	openCodeAgentName                = "opencode"
	claudeStateDirectoryName         = ".claude"
	openCodeStateDirectoryName       = ".opencode"
	openCodeDirectoryName            = "opencode"
	agentInstructionFileName         = "AGENTS.md"
	claudeInstructionFileName        = "CLAUDE.md"
	agentWrapperRoot                 = "/tmp/lja"
	snapCommand                      = "snap"
	snapNodePackage                  = "node"
	snapClassicFlag                  = "--classic"
	sudoCommand                      = "sudo"
	npmCommand                       = "npm"
	installCommand                   = "install"
	npmGlobalFlag                    = "-g"
	aptGetCommand                    = "apt-get"
	codeXHomeEnvironment             = "CODEX_HOME"
	claudeConfigEnvironment          = "CLAUDE_CONFIG_DIR"
	openCodeConfigEnvironment        = "OPENCODE_CONFIG_DIR"
	openCodeConfigContentEnvironment = "OPENCODE_CONFIG_CONTENT"
	openCodePermissionConfig         = `{"permission":"allow"}`
	codeXPermissionFlag              = "--dangerously-bypass-approvals-and-sandbox"
	claudePermissionFlag             = "--dangerously-skip-permissions"
	codeXExecSubcommand              = "exec"
	claudeAuthSubcommand             = "auth"
	codeXLoginSubcommand             = "login"
)

var agentStateEnvironmentNames = []string{
	codeXHomeEnvironment,
	claudeConfigEnvironment,
	openCodeConfigEnvironment,
	xdgConfigHomeEnv,
	xdgDataHomeEnv,
	xdgStateHomeEnv,
	xdgCacheHomeEnv,
	openCodeConfigContentEnvironment,
}

type AgentSpec struct {
	Name           string
	Package        string
	Executable     string
	PermissionFlag string
}

func AgentSpecByName(name string) (AgentSpec, error) {
	switch name {
	case codexAgentName:
		return AgentSpec{Name: name, Package: "@openai/codex", Executable: codexAgentName, PermissionFlag: codeXPermissionFlag}, nil
	case claudeAgentName:
		return AgentSpec{Name: name, Package: "@anthropic-ai/claude-code", Executable: claudeAgentName, PermissionFlag: claudePermissionFlag}, nil
	case openCodeAgentName:
		return AgentSpec{Name: name, Package: "opencode-ai", Executable: openCodeAgentName}, nil
	default:
		return AgentSpec{}, ljaError("unknown agent %s; expected one of codex, claude, opencode", name)
	}
}

func agentSpec(name string) (AgentSpec, error) {
	return AgentSpecByName(name)
}

func AgentNames() []string {
	return []string{codexAgentName, claudeAgentName, openCodeAgentName}
}

func normalizeAgentNames(selected string, additional []string) ([]string, error) {
	if _, err := agentSpec(selected); err != nil {
		return nil, err
	}
	unique := make([]string, 0, len(additional)+1)
	seen := make(map[string]bool, len(additional)+1)
	for _, name := range append([]string{selected}, additional...) {
		if _, err := agentSpec(name); err != nil {
			return nil, err
		}
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}
	return unique, nil
}

func NormalizeAgentNames(selected string, additional []string) ([]string, error) {
	return normalizeAgentNames(selected, additional)
}

type WorkflowOptions struct {
	gpg           *gpgWorkflow
	Recreate      bool
	StateRoot     string
	Development   *DevelopmentConfig
	Environment   map[string]string
	LimaCommand   string
	LockDirectory string
}

func (options WorkflowOptions) limaCommand() string {
	if options.LimaCommand == "" {
		return limaCtlCommand
	}
	return options.LimaCommand
}

func developmentConfigOrDefault(config *DevelopmentConfig) DevelopmentConfig {
	if config == nil {
		defaultConfig := DefaultDevelopmentConfig()
		return defaultConfig
	}
	return config.clone()
}

func guestPackageInstalled(project string, vmName string, packageName string, limactlCommand string) (bool, error) {
	options := defaultProcessOptions(limactlCommand)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{"dpkg-query", "--show", "--showformat=${Status}", packageName}, options, nil)
	if err != nil {
		return false, err
	}
	if result.ExitCode == 1 {
		if err := verifyGuestConnection(project, vmName, limactlCommand); err != nil {
			return false, err
		}
		return false, nil
	}
	if result.ExitCode != 0 {
		if err := verifyGuestConnection(project, vmName, limactlCommand); err != nil {
			return false, err
		}
		return false, ljaError("package query failed for %s: exit status %d", packageName, result.ExitCode)
	}
	return strings.TrimSpace(string(result.Stdout)) == packageInstalledStatus, nil
}

func effectiveDevelopmentPackages(config DevelopmentConfig) []string {
	packages := append([]string{}, config.Packages...)
	if config.GPGForwarding {
		packages = append(packages, gpgPackage)
	}
	if config.CopyGitConfig {
		packages = append(packages, gitCommand)
	}
	return uniqueStrings(packages)
}

func guestPackageInstallationScript(packageNames []string) string {
	quotedPackages := make([]string, 0, len(packageNames))
	for _, packageName := range packageNames {
		quotedPackages = append(quotedPackages, shellQuote(packageName))
	}
	return fmt.Sprintf(
		"%s %s update && %s %s %s -y %s",
		sudoCommand,
		aptGetCommand,
		sudoCommand,
		aptGetCommand,
		installCommand,
		strings.Join(quotedPackages, " "),
	)
}

func ensureGuestPackages(project string, vmName string, packageNames []string, limactlCommand string) error {
	missing := make([]string, 0, len(packageNames))
	for _, packageName := range uniqueStrings(packageNames) {
		installed, err := guestPackageInstalled(project, vmName, packageName, limactlCommand)
		if err != nil {
			return ljaError("cannot prepare package %s in VM %s: %w", packageName, vmName, err)
		}
		if !installed {
			missing = append(missing, packageName)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	slog.Info("installing packages", "packages", missing, "vm", vmName)
	options := defaultProcessOptions(limactlCommand)
	if _, err := runGuest(project, vmName, []string{
		shellCommand,
		"-eu",
		shellCommandFlag,
		guestPackageInstallationScript(missing),
	}, options, nil); err != nil {
		return ljaError("cannot prepare packages %s in VM %s: %w", strings.Join(missing, ", "), vmName, err)
	}
	for _, packageName := range missing {
		installed, err := guestPackageInstalled(project, vmName, packageName, limactlCommand)
		if err != nil {
			return ljaError("cannot prepare package %s in VM %s: %w", packageName, vmName, err)
		}
		if !installed {
			return ljaError("cannot prepare package %s in VM %s: installation did not provide the requested package", packageName, vmName)
		}
	}
	return nil
}

func ensureNodeRuntime(project string, vmName string, limactlCommand string, contexts ...context.Context) error {
	nodeAvailable, err := guestExecutableAvailable(project, vmName, snapNodePackage, limactlCommand, contexts...)
	if err != nil {
		return err
	}
	npmAvailable, err := guestExecutableAvailable(project, vmName, npmCommand, limactlCommand, contexts...)
	if err != nil {
		return err
	}
	if nodeAvailable && npmAvailable {
		return nil
	}
	snapAvailable, err := guestExecutableAvailable(project, vmName, snapCommand, limactlCommand, contexts...)
	if err != nil {
		return err
	}
	if !snapAvailable {
		return ljaError("guest prerequisite %s is missing in VM %s", snapCommand, vmName)
	}
	slog.Info("installing Node", "vm", vmName)
	options := defaultProcessOptions(limactlCommand, contexts...)
	if _, err := runGuest(project, vmName, []string{sudoCommand, snapCommand, "install", snapNodePackage, snapClassicFlag}, options, nil); err != nil {
		return err
	}
	for _, executable := range []string{snapNodePackage, npmCommand} {
		available, err := guestExecutableAvailable(project, vmName, executable, limactlCommand, contexts...)
		if err != nil {
			return err
		}
		if !available {
			return ljaError("Node installation completed but guest executable %s is still missing in VM %s", executable, vmName)
		}
	}
	return nil
}

func installAgentPackage(project string, vmName string, agent AgentSpec, limactlCommand string, contexts ...context.Context) error {
	slog.Info("installing agent", "agent", agent.Name, "package", agent.Package, "vm", vmName)
	options := defaultProcessOptions(limactlCommand, contexts...)
	_, err := runGuest(project, vmName, []string{sudoCommand, npmCommand, installCommand, npmGlobalFlag, agent.Package}, options, nil)
	return err
}

func installAgentLocked(project string, vmName string, agent AgentSpec, update bool, limactlCommand string, contexts ...context.Context) (string, error) {
	if err := verifyGuestConnection(project, vmName, limactlCommand, contexts...); err != nil {
		return "", err
	}
	executablePath, err := guestExecutablePath(project, vmName, agent.Executable, limactlCommand, contexts...)
	if err != nil {
		return "", err
	}
	if executablePath != "" && !update {
		slog.Debug("reusing installed agent", "agent", agent.Name, "vm", vmName)
		return executablePath, nil
	}
	if err := ensureNodeRuntime(project, vmName, limactlCommand, contexts...); err != nil {
		return "", err
	}
	if err := installAgentPackage(project, vmName, agent, limactlCommand, contexts...); err != nil {
		return "", err
	}
	executablePath, err = guestExecutablePath(project, vmName, agent.Executable, limactlCommand, contexts...)
	if err != nil {
		return "", err
	}
	if executablePath == "" {
		return "", ljaError("agent package %s installed but guest executable %s is missing in VM %s", agent.Package, agent.Executable, vmName)
	}
	return executablePath, nil
}

func prepareDevelopment(project string, vmName string, config *DevelopmentConfig, environment map[string]string, limactlCommand string, workflows ...*gpgWorkflow) (returnErr error) {
	actualConfig := developmentConfigOrDefault(config)
	var workflow *gpgWorkflow
	if len(workflows) > 0 {
		workflow = workflows[0]
	}
	if actualConfig.GPGForwarding && workflow == nil {
		options := WorkflowOptions{Development: &actualConfig, Environment: environment}
		cleanup, err := options.ownGPGWorkflow()
		if err != nil {
			return err
		}
		defer cleanup(&returnErr)
		workflow = options.gpg
	}
	if err := ensureGuestPackages(project, vmName, effectiveDevelopmentPackages(actualConfig), limactlCommand); err != nil {
		return err
	}
	if actualConfig.CopyGitConfig {
		if err := prepareGit(project, vmName, limactlCommand, actualConfig.GPGForwarding); err != nil {
			return err
		}
	}
	var err error
	environment, err = workflow.environment(project, vmName, limactlCommand, environment)
	if err != nil {
		return err
	}
	for _, setup := range actualConfig.Setup {
		slog.Info("running setup", "source", setup.Source, "command_index", setup.Index, "vm", vmName)
		options := workflow.processOptions(limactlCommand)
		if _, err := runGuest(project, vmName, []string{shellCommand, "-eu", shellCommandFlag, setup.Command}, options, environment); err != nil {
			return ljaError("setup %s command %d failed: %w", setup.Source, setup.Index, err)
		}
	}
	return nil
}

func PrepareVM(project string, options WorkflowOptions) (returnedInstance LimaInstance, returnErr error) {
	cleanup, err := options.ownGPGWorkflow()
	if err != nil {
		return LimaInstance{}, err
	}
	defer cleanup(&returnErr)

	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return LimaInstance{}, err
	}
	if err := validateProjectDirectory(canonicalProject); err != nil {
		return LimaInstance{}, err
	}
	vmName, err := projectVMName(canonicalProject)
	if err != nil {
		return LimaInstance{}, err
	}
	returnValue := LimaInstance{}
	lockErr := withAdvisoryLock(canonicalProject, vmName, options.StateRoot, options.LockDirectory, func(string) error {
		instance, prepareErr := options.prepareVMLocked(canonicalProject, vmName)
		if prepareErr != nil {
			return prepareErr
		}
		returnValue = instance
		return nil
	})
	if lockErr != nil {
		return LimaInstance{}, lockErr
	}
	return returnValue, nil
}

func InstallAgent(project string, agentName string, update bool, options WorkflowOptions) (returnedInstance LimaInstance, returnErr error) {
	cleanup, err := options.ownGPGWorkflow()
	if err != nil {
		return LimaInstance{}, err
	}
	defer cleanup(&returnErr)

	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return LimaInstance{}, err
	}
	agent, err := agentSpec(agentName)
	if err != nil {
		return LimaInstance{}, err
	}
	if err := validateProjectDirectory(canonicalProject); err != nil {
		return LimaInstance{}, err
	}
	vmName, err := projectVMName(canonicalProject)
	if err != nil {
		return LimaInstance{}, err
	}
	returnValue := LimaInstance{}
	lockErr := withAdvisoryLock(canonicalProject, vmName, options.StateRoot, options.LockDirectory, func(string) error {
		instance, prepareErr := options.prepareVMLocked(canonicalProject, vmName)
		if prepareErr != nil {
			return prepareErr
		}
		if _, installErr := installAgentLocked(canonicalProject, vmName, agent, update, options.limaCommand(), options.gpg.context()); installErr != nil {
			return installErr
		}
		returnValue = instance
		return nil
	})
	if lockErr != nil {
		return LimaInstance{}, lockErr
	}
	return returnValue, nil
}

type AgentExecutable struct {
	Name string
	Path string
}

func agentWrapperDirectory(vmName string) string {
	return filepath.Join(agentWrapperRoot, vmName, "bin")
}

func AgentWrapperDirectory(vmName string) string {
	return agentWrapperDirectory(vmName)
}

func permissionOptions(agentName string) []string {
	if agentName == codexAgentName {
		return []string{codeXPermissionFlag, "--yolo", "--ask-for-approval", "-a", "--sandbox", "-s"}
	}
	return []string{claudePermissionFlag, "--allow-dangerously-skip-permissions", "--permission-mode"}
}

func permissionOptionPatterns(agentName string) []string {
	patterns := make([]string, 0)
	for _, option := range permissionOptions(agentName) {
		patterns = append(patterns, option)
		if strings.HasPrefix(option, "--") {
			patterns = append(patterns, option+"=*")
		}
	}
	return patterns
}

func orderedAgentEnvironment(stateRoot string, agentName string) ([][2]string, error) {
	values, err := agentStateEnvironment(stateRoot, agentName)
	if err != nil {
		return nil, err
	}
	order := []string{}
	if agentName == codexAgentName {
		order = []string{codeXHomeEnvironment}
	} else if agentName == claudeAgentName {
		order = []string{claudeConfigEnvironment}
	} else {
		order = []string{openCodeConfigEnvironment, xdgConfigHomeEnv, xdgDataHomeEnv, xdgStateHomeEnv, xdgCacheHomeEnv}
	}
	entries := make([][2]string, 0, len(order)+1)
	for _, name := range order {
		entries = append(entries, [2]string{name, values[name]})
	}
	if agentName == openCodeAgentName {
		entries = append(entries, [2]string{openCodeConfigContentEnvironment, openCodePermissionConfig})
	}
	return entries, nil
}

func agentWrapperContent(stateRoot string, agentName string, executablePath string) (string, error) {
	agent, err := agentSpec(agentName)
	if err != nil {
		return "", err
	}
	environment, err := orderedAgentEnvironment(stateRoot, agentName)
	if err != nil {
		return "", err
	}
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		"unset " + strings.Join(agentStateEnvironmentNames, " "),
	}
	for _, entry := range environment {
		lines = append(lines, "export "+entry[0]+"="+shellQuote(entry[1]))
	}
	quotedExecutable := shellQuote(executablePath)
	if agent.PermissionFlag == "" {
		return strings.Join(append(lines, "exec "+quotedExecutable+" \"$@\""), "\n") + "\n", nil
	}
	patterns := strings.Join(permissionOptionPatterns(agentName), "|")
	loginSubcommand := codeXLoginSubcommand
	if agentName == claudeAgentName {
		loginSubcommand = claudeAuthSubcommand
	}
	lines = append(lines,
		"has_explicit_permission_option() {",
		"    for argument in \"$@\"; do",
		"        case \"$argument\" in",
		"            "+patterns+") return 0 ;;",
		"        esac",
		"    done",
		"    return 1",
		"}",
		"if [ \"${1-}\" = "+shellQuote(loginSubcommand)+" ]; then",
		"    exec "+quotedExecutable+" \"$@\"",
		"fi",
		"if has_explicit_permission_option \"$@\"; then",
		"    exec "+quotedExecutable+" \"$@\"",
		"fi",
	)
	if agentName == codexAgentName {
		lines = append(lines,
			"if [ \"${1-}\" = "+shellQuote(codeXExecSubcommand)+" ]; then",
			"    first_argument=\"$1\"",
			"    shift",
			"    exec "+quotedExecutable+" \"$first_argument\" "+shellQuote(agent.PermissionFlag)+" \"$@\"",
			"fi",
		)
	}
	lines = append(lines, "exec "+quotedExecutable+" "+shellQuote(agent.PermissionFlag)+" \"$@\"")
	return strings.Join(lines, "\n") + "\n", nil
}

func AgentWrapperContent(stateRoot string, agentName string, executablePath string) (string, error) {
	return agentWrapperContent(stateRoot, agentName, executablePath)
}

func agentWrapperSetupScript(stateRoot string, vmName string, executables []AgentExecutable) (string, error) {
	if len(executables) == 0 {
		return "", ljaError("cannot prepare agent wrappers without requested agents")
	}
	wrapperDirectory := agentWrapperDirectory(vmName)
	lines := []string{
		"set -eu",
		"temporary_path=",
		"cleanup() {",
		"    if [ -n \"$temporary_path\" ]; then",
		"        rm -f -- \"$temporary_path\"",
		"    fi",
		"}",
		"trap cleanup EXIT HUP INT TERM",
		"wrapper_directory=" + shellQuote(wrapperDirectory),
		"mkdir -p -- \"$wrapper_directory\"",
		"chmod 700 \"$wrapper_directory\"",
	}
	for _, executable := range executables {
		content, err := agentWrapperContent(stateRoot, executable.Name, executable.Path)
		if err != nil {
			return "", err
		}
		wrapperPath := filepath.Join(wrapperDirectory, executable.Name)
		lines = append(lines,
			"temporary_path=$(mktemp "+shellQuote(wrapperPath+".XXXXXX")+")",
			"printf '%s' "+shellQuote(content)+" > \"$temporary_path\"",
			"chmod 700 \"$temporary_path\"",
			"mv -f -- \"$temporary_path\" "+shellQuote(wrapperPath),
			"temporary_path=",
		)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func PrepareAgentWrappers(project string, vmName string, stateRoot string, executables []AgentExecutable, limactlCommand string) error {
	script, err := agentWrapperSetupScript(stateRoot, vmName, executables)
	if err != nil {
		return err
	}
	options := defaultProcessOptions(limactlCommand)
	_, err = runGuest(project, vmName, []string{shellCommand, shellCommandFlag, script}, options, nil)
	return err
}

func prepareAgents(project string, selectedAgent string, withAgents []string, trustDirectories []string, options WorkflowOptions) (returnedInstance LimaInstance, returnErr error) {
	cleanup, err := options.ownGPGWorkflow()
	if err != nil {
		return LimaInstance{}, err
	}
	defer cleanup(&returnErr)

	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return LimaInstance{}, err
	}
	agentNames, err := normalizeAgentNames(selectedAgent, withAgents)
	if err != nil {
		return LimaInstance{}, err
	}
	if err := validateProjectDirectory(canonicalProject); err != nil {
		return LimaInstance{}, err
	}
	vmName, err := projectVMName(canonicalProject)
	if err != nil {
		return LimaInstance{}, err
	}
	if trustDirectories == nil {
		trustDirectories = []string{canonicalProject}
	}
	stateRoot := options.StateRoot
	if stateRoot == "" {
		stateRoot = canonicalProject
	}
	returnValue := LimaInstance{}
	lockErr := withAdvisoryLock(canonicalProject, vmName, options.StateRoot, options.LockDirectory, func(string) error {
		instance, prepareErr := options.prepareVMLocked(canonicalProject, vmName)
		if prepareErr != nil {
			return prepareErr
		}
		executables := make([]AgentExecutable, 0, len(agentNames))
		for _, agentName := range agentNames {
			agent, agentErr := agentSpec(agentName)
			if agentErr != nil {
				return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, agentErr)
			}
			executablePath, installErr := installAgentLocked(canonicalProject, vmName, agent, false, options.limaCommand(), options.gpg.context())
			if installErr != nil {
				return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, installErr)
			}
			if _, stateErr := EnsureAgentStateDirectories(stateRoot, agentName); stateErr != nil {
				return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, stateErr)
			}
			if _, instructionErr := RefreshAgentInstructions(stateRoot, agentName, nil); instructionErr != nil {
				return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, instructionErr)
			}
			if agentName == codexAgentName && len(trustDirectories) != 0 {
				if trustErr := ensureCodexDirectoryTrust(stateRoot, trustDirectories, options.LockDirectory); trustErr != nil {
					return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, trustErr)
				}
			}
			executables = append(executables, AgentExecutable{Name: agentName, Path: executablePath})
		}
		if len(agentNames) > 1 {
			if wrapperErr := PrepareAgentWrappers(canonicalProject, vmName, stateRoot, executables, options.limaCommand()); wrapperErr != nil {
				return ljaError("cannot prepare agent wrappers in VM %s: %w", vmName, wrapperErr)
			}
		}
		returnValue = instance
		return nil
	})
	if lockErr != nil {
		return LimaInstance{}, lockErr
	}
	return returnValue, nil
}

func PrepareAgents(project string, selectedAgent string, withAgents []string, trustDirectories []string, options WorkflowOptions) (LimaInstance, error) {
	return prepareAgents(project, selectedAgent, withAgents, trustDirectories, options)
}

func isLoginCommand(agentName string, arguments []string) bool {
	if len(arguments) == 0 {
		return false
	}
	if agentName == codexAgentName {
		return arguments[0] == codeXLoginSubcommand
	}
	if agentName == claudeAgentName {
		return arguments[0] == claudeAuthSubcommand
	}
	return false
}

func hasOption(arguments []string, options []string) bool {
	for _, argument := range arguments {
		for _, option := range options {
			if argument == option || strings.HasPrefix(argument, option+"=") {
				return true
			}
		}
	}
	return false
}

func BuildAgentInvocation(agentName string, arguments []string) ([]string, map[string]string, error) {
	agent, err := agentSpec(agentName)
	if err != nil {
		return nil, nil, err
	}
	invocation := append([]string{}, arguments...)
	environment := make(map[string]string)
	if agentName == openCodeAgentName {
		environment[openCodeConfigContentEnvironment] = openCodePermissionConfig
	} else if agent.PermissionFlag != "" && !isLoginCommand(agentName, arguments) && !hasOption(invocation, permissionOptions(agentName)) {
		insertAt := 0
		if agentName == codexAgentName && len(invocation) > 0 && invocation[0] == codeXExecSubcommand {
			insertAt = 1
		}
		invocation = append(invocation, "")
		copy(invocation[insertAt+1:], invocation[insertAt:])
		invocation[insertAt] = agent.PermissionFlag
	}
	return invocation, environment, nil
}

func RunAgent(project string, agentName string, arguments []string, withAgents []string, workingDirectory string, options WorkflowOptions) (exitCode int, returnErr error) {
	cleanup, err := options.ownGPGWorkflow()
	if err != nil {
		return 0, err
	}
	defer cleanup(&returnErr)

	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return 0, err
	}
	if _, err := sortedEnvironmentArguments(options.Environment); err != nil {
		return 0, err
	}
	if workingDirectory == "" {
		workingDirectory = canonicalProject
	} else {
		workingDirectory, err = canonicalProjectPath(workingDirectory)
		if err != nil {
			return 0, err
		}
	}
	trustDirectories := []string{canonicalProject, workingDirectory}
	if isLoginCommand(agentName, arguments) {
		trustDirectories = []string{}
	}
	instance, err := prepareAgents(canonicalProject, agentName, withAgents, trustDirectories, options)
	if err != nil {
		return 0, err
	}
	invocation, agentEnvironment, err := BuildAgentInvocation(agentName, arguments)
	if err != nil {
		return 0, err
	}
	stateRoot := options.StateRoot
	if stateRoot == "" {
		stateRoot = canonicalProject
	}
	stateEnvironment, err := agentStateEnvironment(stateRoot, agentName)
	if err != nil {
		return 0, err
	}
	environment := make(map[string]string, len(options.Environment)+len(stateEnvironment)+len(agentEnvironment))
	for name, value := range options.Environment {
		environment[name] = value
	}
	for name, value := range stateEnvironment {
		environment[name] = value
	}
	for name, value := range agentEnvironment {
		environment[name] = value
	}
	slog.Info("launching agent", "agent", agentName, "vm", instance.Name)
	agent, err := agentSpec(agentName)
	if err != nil {
		return 0, err
	}
	guestArguments := []string{agent.Executable}
	if len(withAgents) != 0 {
		names, normalizeErr := normalizeAgentNames(agentName, withAgents)
		if normalizeErr != nil {
			return 0, normalizeErr
		}
		if len(names) > 1 {
			wrapperDirectory := agentWrapperDirectory(instance.Name)
			launchScript := "export PATH=" + shellQuote(wrapperDirectory) + ":${PATH-}\nexec " + shellQuote(agent.Executable) + " \"$@\"\n"
			guestArguments = []string{shellCommand, shellCommandFlag, launchScript, programName}
		}
	}
	guestArguments = append(guestArguments, invocation...)
	environment, err = options.gpg.environment(canonicalProject, instance.Name, options.limaCommand(), environment)
	if err != nil {
		return 0, err
	}
	optionsForGuest := options.gpg.processOptions(options.limaCommand())
	optionsForGuest.check = false
	result, err := runGuest(workingDirectory, instance.Name, guestArguments, optionsForGuest, environment)
	if err != nil {
		return 0, err
	}
	return result.ExitCode, nil
}

func sortedEnvironmentArguments(environment map[string]string) ([]string, error) {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	arguments := make([]string, 0, len(names))
	for _, name := range names {
		if !environmentNamePattern.MatchString(name) {
			return nil, ljaError("invalid guest environment variable name: %s", name)
		}
		value := environment[name]
		if strings.IndexByte(value, 0) >= 0 {
			return nil, ljaError("guest environment value for %s contains a NUL character", name)
		}
		arguments = append(arguments, name+"="+value)
	}
	return arguments, nil
}

func OpenShell(project string, arguments []string, workingDirectory string, options WorkflowOptions) (exitCode int, returnErr error) {
	cleanup, err := options.ownGPGWorkflow()
	if err != nil {
		return 0, err
	}
	defer cleanup(&returnErr)

	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return 0, err
	}
	if workingDirectory == "" {
		workingDirectory = canonicalProject
	} else {
		workingDirectory, err = canonicalProjectPath(workingDirectory)
		if err != nil {
			return 0, err
		}
	}
	if _, err := sortedEnvironmentArguments(options.Environment); err != nil {
		return 0, err
	}
	instance, err := PrepareVM(canonicalProject, options)
	if err != nil {
		return 0, err
	}
	environment, err := options.gpg.environment(canonicalProject, instance.Name, options.limaCommand(), options.Environment)
	if err != nil {
		return 0, err
	}
	environmentArguments, err := sortedEnvironmentArguments(environment)
	if err != nil {
		return 0, err
	}
	forwarded := append([]string{}, arguments...)
	if len(forwarded) == 0 {
		forwarded = []string{shellCommand, shellCommandFlag, `exec "${SHELL:-/bin/sh}" -l`}
	}
	forwarded = guestArgumentsWithUserPath(forwarded)
	limaArguments := []string{"shell", limaWorkdirFlag, workingDirectory, instance.Name}
	limaArguments = append(limaArguments, environmentArguments...)
	limaArguments = append(limaArguments, forwarded...)
	runOptions := options.gpg.processOptions(options.limaCommand())
	runOptions.check = false
	result, err := runLima(limaArguments, runOptions)
	if err != nil {
		return 0, err
	}
	return result.ExitCode, nil
}

func formatAgentDescription(agentName string, arguments []string, withAgents []string) string {
	prepared := 1
	if names, err := normalizeAgentNames(agentName, withAgents); err == nil {
		prepared = len(names)
	}
	return fmt.Sprintf("launch %s with %d forwarded argument(s) and %d prepared agent(s)", agentName, len(arguments), prepared)
}
