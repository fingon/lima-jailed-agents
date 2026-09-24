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
	nodeCommand                      = "node"
	sudoCommand                      = "sudo"
	npmCommand                       = "npm"
	installCommand                   = "install"
	npmGlobalFlag                    = "-g"
	npmEngineStrictFlag              = "--engine-strict"
	versionFlag                      = "--version"
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
	gpg                   *gpgWorkflow
	github                *githubWorkflow
	Recreate              bool
	StateRoot             string
	Development           *DevelopmentConfig
	Environment           map[string]string
	hostEnvironment       map[string]string
	hostHome              string
	LimaCommand           string
	LockDirectory         string
	agentTrustDirectories []string
	agentUpdateName       string
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

func configuredAgentNames(config *DevelopmentConfig) ([]string, error) {
	actualConfig := developmentConfigOrDefault(config)
	names := uniqueStrings(actualConfig.Agents)
	for _, name := range names {
		if _, err := agentSpec(name); err != nil {
			return nil, err
		}
	}
	return names, nil
}

func effectiveAgentNames(selectedAgent string, withAgents []string, config *DevelopmentConfig) ([]string, error) {
	configured, err := configuredAgentNames(config)
	if err != nil {
		return nil, err
	}
	additional := make([]string, 0, len(configured)+len(withAgents))
	additional = append(additional, configured...)
	additional = append(additional, withAgents...)
	return normalizeAgentNames(selectedAgent, additional)
}

func effectiveDevelopmentPackages(config DevelopmentConfig) []string {
	return (guestPackageBackend{gitPackage: gitCommand, ghPackage: ghCommand, gpgPackage: gpgPackage}).developmentPackageNames(config)
}

func guestExecutableVersion(project string, vmName string, executable string, limactlCommand string, contexts ...context.Context) (string, string, error) {
	executablePath, err := guestExecutablePath(project, vmName, executable, limactlCommand, contexts...)
	if err != nil {
		return "", "", err
	}
	if executablePath == "" {
		return "", "", nil
	}
	options := defaultProcessOptions(limactlCommand, contexts...)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{executable, versionFlag}, options, nil)
	if err != nil {
		return "", "", ljaError("cannot run guest executable %s in VM %s: %w", executable, vmName, err)
	}
	if result.ExitCode != 0 {
		if connectionErr := verifyGuestConnection(project, vmName, limactlCommand, contexts...); connectionErr != nil {
			return "", "", ljaError("guest executable %s is present but failed in VM %s: %w", executable, vmName, connectionErr)
		}
		detail := processOutput(result)
		if detail != "" {
			return "", "", ljaError("guest executable %s is present but failed in VM %s: exit status %d: %s", executable, vmName, result.ExitCode, detail)
		}
		return "", "", ljaError("guest executable %s is present but failed in VM %s: exit status %d", executable, vmName, result.ExitCode)
	}
	version := strings.TrimSpace(string(result.Stdout))
	if version == "" {
		return "", "", ljaError("guest executable %s in VM %s returned no version", executable, vmName)
	}
	if strings.IndexByte(version, 0) >= 0 || strings.ContainsAny(version, "\r\n") {
		return "", "", ljaError("guest executable %s in VM %s returned an invalid version", executable, vmName)
	}
	return executablePath, version, nil
}

func nodeRuntimeDescription(project string, vmName string, limactlCommand string, contexts ...context.Context) (string, error) {
	_, nodeVersion, err := guestExecutableVersion(project, vmName, nodeCommand, limactlCommand, contexts...)
	if err != nil {
		return "", err
	}
	_, npmVersion, err := guestExecutableVersion(project, vmName, npmCommand, limactlCommand, contexts...)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("node=%s npm=%s", nodeVersion, npmVersion), nil
}

func ensureNodeRuntime(project string, vmName string, limactlCommand string, contexts ...context.Context) error {
	nodePath, _, err := guestExecutableVersion(project, vmName, nodeCommand, limactlCommand, contexts...)
	if err != nil {
		return err
	}
	npmPath, _, err := guestExecutableVersion(project, vmName, npmCommand, limactlCommand, contexts...)
	if err != nil {
		return err
	}
	if nodePath != "" && npmPath != "" {
		return nil
	}
	backend, err := packageBackendForGuest(project, vmName, limactlCommand, contexts...)
	if err != nil {
		return ljaError("cannot prepare Node runtime in VM %s: %w", vmName, err)
	}
	packages := backend.nodePackageNames()
	if len(packages) == 0 {
		return ljaError("%s backend has no Node runtime dependencies for VM %s", backend.name, vmName)
	}
	slog.Info("installing Node runtime", "backend", backend.name, "packages", packages, "vm", vmName)
	if err := ensureGuestPackagesWithBackend(project, vmName, packages, backend, limactlCommand, contexts...); err != nil {
		return err
	}
	for _, executable := range []string{nodeCommand, npmCommand} {
		path, _, probeErr := guestExecutableVersion(project, vmName, executable, limactlCommand, contexts...)
		if probeErr != nil {
			return probeErr
		}
		if path == "" {
			return ljaError("Node runtime installation with %s backend in VM %s did not provide executable %s", backend.name, vmName, executable)
		}
	}
	return nil
}

func npmEngineFailure(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "ebadengine") || strings.Contains(lower, "unsupported engine") || (strings.Contains(lower, "engine") && strings.Contains(lower, "node"))
}

func installAgentPackage(project string, vmName string, agent AgentSpec, limactlCommand string, contexts ...context.Context) error {
	slog.Info("installing agent", "agent", agent.Name, "package", agent.Package, "vm", vmName)
	options := defaultProcessOptions(limactlCommand, contexts...)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{sudoCommand, npmCommand, installCommand, npmEngineStrictFlag, npmGlobalFlag, agent.Package}, options, nil)
	if err != nil {
		return ljaError("cannot install agent package %s in VM %s: %w", agent.Package, vmName, err)
	}
	if result.ExitCode == 0 {
		return nil
	}
	detail := processOutput(result)
	runtime, runtimeErr := nodeRuntimeDescription(project, vmName, limactlCommand, contexts...)
	if runtimeErr != nil {
		runtime = fmt.Sprintf("runtime version lookup failed: %v", runtimeErr)
	}
	if npmEngineFailure(detail) {
		return ljaError("cannot install agent package %s in VM %s: npm engine requirements are incompatible with guest runtime (%s); select a newer template or use custom provisioning through setup or lima.provision to install a compatible system-wide Node runtime: %s", agent.Package, vmName, runtime, detail)
	}
	if detail != "" {
		return ljaError("cannot install agent package %s in VM %s with guest runtime (%s): exit status %d: %s", agent.Package, vmName, runtime, result.ExitCode, detail)
	}
	return ljaError("cannot install agent package %s in VM %s with guest runtime (%s): exit status %d", agent.Package, vmName, runtime, result.ExitCode)
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

func workflowProcessOptions(limactlCommand string, gpg *gpgWorkflow, github *githubWorkflow) processOptions {
	options := gpg.processOptions(limactlCommand)
	if options.context == nil && github != nil {
		options.context = github.context()
	}
	return options
}

func prepareDevelopment(project string, vmName string, config *DevelopmentConfig, environment map[string]string, limactlCommand string, gpg *gpgWorkflow, github *githubWorkflow) (returnErr error) {
	actualConfig := developmentConfigOrDefault(config)
	workflow := gpg
	if actualConfig.GPGForwarding && workflow == nil {
		options := WorkflowOptions{Development: &actualConfig, Environment: environment}
		cleanup, err := options.ownGPGWorkflow()
		if err != nil {
			return err
		}
		defer cleanup(&returnErr)
		workflow = options.gpg
	}
	backend, err := ensureDevelopmentPackages(project, vmName, actualConfig, limactlCommand)
	if err != nil {
		return err
	}
	if err := verifyDevelopmentExecutables(project, vmName, actualConfig, backend, limactlCommand); err != nil {
		return err
	}
	if actualConfig.CopyGitConfig {
		prepareGitFunction := prepareGit
		if actualConfig.GitHub.Enabled {
			prepareGitFunction = prepareGitWithGitHub
		}
		if err := prepareGitFunction(project, vmName, limactlCommand, actualConfig.GPGForwarding); err != nil {
			return err
		}
	}
	environment, err = workflow.environment(project, vmName, limactlCommand, environment)
	if err != nil {
		return err
	}
	environment, err = github.environment(environment)
	if err != nil {
		return err
	}
	for _, setup := range actualConfig.Setup {
		slog.Info("running setup", "source", setup.Source, "command_index", setup.Index, "vm", vmName)
		options := workflowProcessOptions(limactlCommand, workflow, github)
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
	githubCleanup, err := options.ownGitHubWorkflow()
	if err != nil {
		return LimaInstance{}, err
	}
	defer githubCleanup(&returnErr)

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
	githubCleanup, err := options.ownGitHubWorkflow()
	if err != nil {
		return LimaInstance{}, err
	}
	defer githubCleanup(&returnErr)

	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return LimaInstance{}, err
	}
	agent, err := agentSpec(agentName)
	if err != nil {
		return LimaInstance{}, err
	}
	configured, err := configuredAgentNames(options.Development)
	if err != nil {
		return LimaInstance{}, err
	}
	configuredAgents := make(map[string]bool, len(configured))
	for _, name := range configured {
		configuredAgents[name] = true
	}
	if update && configuredAgents[agentName] {
		options.agentUpdateName = agentName
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
		if !configuredAgents[agentName] {
			if _, installErr := installAgentLocked(canonicalProject, vmName, agent, update, options.limaCommand(), options.gpg.context()); installErr != nil {
				return installErr
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
	requested := make(map[string]bool, len(executables))
	for _, executable := range executables {
		if _, err := agentSpec(executable.Name); err != nil {
			return "", err
		}
		requested[executable.Name] = true
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
	for _, agentName := range AgentNames() {
		if !requested[agentName] {
			lines = append(lines, "rm -f -- "+shellQuote(filepath.Join(wrapperDirectory, agentName)))
		}
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

func prepareAgentNamesLocked(project string, vmName string, stateRoot string, agentNames []string, trustDirectories []string, lockDirectory string, limactlCommand string, prepareWrappers bool, updateAgentName string, contexts ...context.Context) error {
	if stateRoot == "" {
		stateRoot = project
	}
	executables := make([]AgentExecutable, 0, len(agentNames))
	codexRequested := false
	for _, agentName := range agentNames {
		agent, err := agentSpec(agentName)
		if err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, err)
		}
		executablePath, err := installAgentLocked(project, vmName, agent, agentName == updateAgentName, limactlCommand, contexts...)
		if err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, err)
		}
		if _, err := EnsureAgentStateDirectories(stateRoot, agentName); err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, err)
		}
		if _, err := RefreshAgentInstructions(stateRoot, agentName, nil); err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", agentName, vmName, err)
		}
		if agentName == codexAgentName {
			codexRequested = true
		}
		executables = append(executables, AgentExecutable{Name: agentName, Path: executablePath})
	}
	if codexRequested && len(trustDirectories) != 0 {
		if err := ensureCodexDirectoryTrust(stateRoot, trustDirectories, lockDirectory); err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", codexAgentName, vmName, err)
		}
	}
	if prepareWrappers && len(executables) != 0 {
		if err := PrepareAgentWrappers(project, vmName, stateRoot, executables, limactlCommand); err != nil {
			return ljaError("cannot prepare agent wrappers in VM %s: %w", vmName, err)
		}
	}
	return nil
}

func prepareConfiguredAgentsLocked(project string, vmName string, stateRoot string, development *DevelopmentConfig, trustDirectories []string, lockDirectory string, limactlCommand string, updateAgentName string, prepareWrappers bool, workflows ...*gpgWorkflow) error {
	agentNames, err := configuredAgentNames(development)
	if err != nil {
		return err
	}
	var contexts []context.Context
	if len(workflows) > 0 && workflows[0] != nil {
		contexts = []context.Context{workflows[0].context()}
	}
	return prepareAgentNamesLocked(project, vmName, stateRoot, agentNames, trustDirectories, lockDirectory, limactlCommand, prepareWrappers, updateAgentName, contexts...)
}

func prepareAgents(project string, selectedAgent string, withAgents []string, trustDirectories []string, options WorkflowOptions) (returnedInstance LimaInstance, returnErr error) {
	cleanup, err := options.ownGPGWorkflow()
	if err != nil {
		return LimaInstance{}, err
	}
	defer cleanup(&returnErr)
	githubCleanup, err := options.ownGitHubWorkflow()
	if err != nil {
		return LimaInstance{}, err
	}
	defer githubCleanup(&returnErr)

	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return LimaInstance{}, err
	}
	agentNames, err := effectiveAgentNames(selectedAgent, withAgents, options.Development)
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
	preparationConfig := developmentConfigOrDefault(options.Development)
	preparationConfig.Agents = []string{}
	options.Development = &preparationConfig
	options.agentTrustDirectories = append([]string{}, trustDirectories...)
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
		if err := prepareAgentNamesLocked(canonicalProject, vmName, stateRoot, agentNames, trustDirectories, options.LockDirectory, options.limaCommand(), len(agentNames) > 1, "", options.gpg.context()); err != nil {
			return err
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
	githubCleanup, err := options.ownGitHubWorkflow()
	if err != nil {
		return 0, err
	}
	defer githubCleanup(&returnErr)

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
	agentNames, err := effectiveAgentNames(agentName, withAgents, options.Development)
	if err != nil {
		return 0, err
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
	if len(agentNames) > 1 {
		wrapperDirectory := agentWrapperDirectory(instance.Name)
		launchScript := "export PATH=" + shellQuote(wrapperDirectory) + ":${PATH-}\nexec " + shellQuote(agent.Executable) + " \"$@\"\n"
		guestArguments = []string{shellCommand, shellCommandFlag, launchScript, programName}
	}
	guestArguments = append(guestArguments, invocation...)
	environment, err = options.gpg.environment(canonicalProject, instance.Name, options.limaCommand(), environment)
	if err != nil {
		return 0, err
	}
	environment, err = options.github.environment(environment)
	if err != nil {
		return 0, err
	}
	optionsForGuest := workflowProcessOptions(options.limaCommand(), options.gpg, options.github)
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
	githubCleanup, err := options.ownGitHubWorkflow()
	if err != nil {
		return 0, err
	}
	defer githubCleanup(&returnErr)

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
	configured, err := configuredAgentNames(options.Development)
	if err != nil {
		return 0, err
	}
	options.agentTrustDirectories = []string{canonicalProject, workingDirectory}
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
	environment, err = options.github.environment(environment)
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
	if len(configured) != 0 {
		forwarded = guestArgumentsWithPath(forwarded, agentWrapperDirectory(instance.Name))
	} else {
		forwarded = guestArgumentsWithUserPath(forwarded)
	}
	limaArguments := []string{"shell", limaWorkdirFlag, workingDirectory, instance.Name}
	limaArguments = append(limaArguments, environmentArguments...)
	limaArguments = append(limaArguments, forwarded...)
	runOptions := workflowProcessOptions(options.limaCommand(), options.gpg, options.github)
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
