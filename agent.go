package lja

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
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

type agentInstallationOptions struct {
	project        string
	vmName         string
	agent          AgentSpec
	update         bool
	limactlCommand string
	contexts       []context.Context
}

type developmentPreparationOptions struct {
	project        string
	vmName         string
	config         *DevelopmentConfig
	environment    map[string]string
	limactlCommand string
	gpg            *gpgWorkflow
	github         *githubWorkflow
}

type agentPreparationOptions struct {
	project          string
	vmName           string
	stateRoot        string
	development      *DevelopmentConfig
	trustDirectories []string
	lockDirectory    string
	limactlCommand   string
	updateAgentName  string
	prepareWrappers  bool
	contexts         []context.Context
}

type AgentRunOptions struct {
	AgentName        string
	Arguments        []string
	WithAgents       []string
	WorkingDirectory string
	Workflow         WorkflowOptions
}

type AgentWrapperOptions struct {
	Project     string
	VMName      string
	StateRoot   string
	Executables []AgentExecutable
	LimaCommand string
}

type AgentPreparationOptions struct {
	Project          string
	SelectedAgent    string
	WithAgents       []string
	TrustDirectories []string
	Workflow         WorkflowOptions
}

func workflowContexts(workflow *gpgWorkflow) []context.Context {
	if workflow == nil {
		return nil
	}
	return []context.Context{workflow.context()}
}

func (options WorkflowOptions) agentPreparation(project, vmName string, prepareWrappers bool, updateAgentName string) agentPreparationOptions {
	return agentPreparationOptions{
		project:          project,
		vmName:           vmName,
		stateRoot:        options.StateRoot,
		development:      options.Development,
		trustDirectories: options.agentTrustDirectories,
		lockDirectory:    options.LockDirectory,
		limactlCommand:   options.limaCommand(),
		updateAgentName:  updateAgentName,
		prepareWrappers:  prepareWrappers,
		contexts:         workflowContexts(options.gpg),
	}
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

func guestExecutableVersion(project, vmName, executable string, options processOptions) (string, string, error) {
	executablePath, err := guestExecutablePath(project, vmName, executable, options)
	if err != nil {
		return "", "", err
	}
	if executablePath == "" {
		return "", "", nil
	}
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{executable, versionFlag}, guestExecution(options, nil))
	if err != nil {
		return "", "", ljaError("cannot run guest executable %s in VM %s: %w", executable, vmName, err)
	}
	if result.ExitCode != 0 {
		if connectionErr := verifyGuestConnection(project, vmName, options); connectionErr != nil {
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

func nodeRuntimeDescription(project, vmName, limactlCommand string, contexts ...context.Context) (string, error) {
	options := defaultProcessOptions(limactlCommand, contexts...)
	_, nodeVersion, err := guestExecutableVersion(project, vmName, nodeCommand, options)
	if err != nil {
		return "", err
	}
	_, npmVersion, err := guestExecutableVersion(project, vmName, npmCommand, options)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("node=%s npm=%s", nodeVersion, npmVersion), nil
}

func ensureNodeRuntime(project, vmName, limactlCommand string, contexts ...context.Context) error {
	guestOptions := defaultProcessOptions(limactlCommand, contexts...)
	nodePath, _, err := guestExecutableVersion(project, vmName, nodeCommand, guestOptions)
	if err != nil {
		return err
	}
	npmPath, _, err := guestExecutableVersion(project, vmName, npmCommand, guestOptions)
	if err != nil {
		return err
	}
	if nodePath != "" && npmPath != "" {
		return nil
	}
	backend, err := packageBackendForGuest(project, vmName, guestOptions)
	if err != nil {
		return ljaError("cannot prepare Node runtime in VM %s: %w", vmName, err)
	}
	packages := backend.nodePackageNames()
	if len(packages) == 0 {
		return ljaError("%s backend has no Node runtime dependencies for VM %s", backend.name, vmName)
	}
	slog.Info("installing Node runtime", "backend", backend.name, "packages", packages, "vm", vmName)
	packageOptions := guestPackageOptions{
		project:        project,
		vmName:         vmName,
		limactlCommand: limactlCommand,
		contexts:       contexts,
	}
	if err := ensureGuestPackagesWithBackend(packageOptions, packages, backend); err != nil {
		return err
	}
	for _, executable := range []string{nodeCommand, npmCommand} {
		path, _, probeErr := guestExecutableVersion(project, vmName, executable, guestOptions)
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

func installAgentPackage(options agentInstallationOptions) error {
	slog.Info("installing agent", "agent", options.agent.Name, "package", options.agent.Package, "vm", options.vmName)
	guestOptions := defaultProcessOptions(options.limactlCommand, options.contexts...)
	guestOptions.captureOutput = true
	guestOptions.check = false
	result, err := runGuest(options.project, options.vmName, []string{sudoCommand, npmCommand, installCommand, npmEngineStrictFlag, npmGlobalFlag, options.agent.Package}, guestExecution(guestOptions, nil))
	if err != nil {
		return ljaError("cannot install agent package %s in VM %s: %w", options.agent.Package, options.vmName, err)
	}
	if result.ExitCode == 0 {
		return nil
	}
	detail := processOutput(result)
	runtime, runtimeErr := nodeRuntimeDescription(options.project, options.vmName, options.limactlCommand, options.contexts...)
	if runtimeErr != nil {
		runtime = fmt.Sprintf("runtime version lookup failed: %v", runtimeErr)
	}
	if npmEngineFailure(detail) {
		return ljaError("cannot install agent package %s in VM %s: npm engine requirements are incompatible with guest runtime (%s); select a newer template or use custom provisioning through setup or lima.provision to install a compatible system-wide Node runtime: %s", options.agent.Package, options.vmName, runtime, detail)
	}
	if detail != "" {
		return ljaError("cannot install agent package %s in VM %s with guest runtime (%s): exit status %d: %s", options.agent.Package, options.vmName, runtime, result.ExitCode, detail)
	}
	return ljaError("cannot install agent package %s in VM %s with guest runtime (%s): exit status %d", options.agent.Package, options.vmName, runtime, result.ExitCode)
}

func installAgentLocked(options agentInstallationOptions) (string, error) {
	guestOptions := defaultProcessOptions(options.limactlCommand, options.contexts...)
	if err := verifyGuestConnection(options.project, options.vmName, guestOptions); err != nil {
		return "", err
	}
	executablePath, err := guestExecutablePath(options.project, options.vmName, options.agent.Executable, guestOptions)
	if err != nil {
		return "", err
	}
	if executablePath != "" && !options.update {
		slog.Debug("reusing installed agent", "agent", options.agent.Name, "vm", options.vmName)
		return executablePath, nil
	}
	if err := ensureNodeRuntime(options.project, options.vmName, options.limactlCommand, options.contexts...); err != nil {
		return "", err
	}
	if err := installAgentPackage(options); err != nil {
		return "", err
	}
	executablePath, err = guestExecutablePath(options.project, options.vmName, options.agent.Executable, guestOptions)
	if err != nil {
		return "", err
	}
	if executablePath == "" {
		return "", ljaError("agent package %s installed but guest executable %s is missing in VM %s", options.agent.Package, options.agent.Executable, options.vmName)
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

func prepareDevelopment(options developmentPreparationOptions) (returnErr error) {
	actualConfig := developmentConfigOrDefault(options.config)
	workflow := options.gpg
	environment := options.environment
	if actualConfig.GPGForwarding && workflow == nil {
		workflowOptions := WorkflowOptions{Development: &actualConfig, Environment: environment}
		cleanup, err := workflowOptions.ownGPGWorkflow()
		if err != nil {
			return err
		}
		defer cleanup(&returnErr)
		workflow = workflowOptions.gpg
	}
	packageOptions := guestPackageOptions{
		project:        options.project,
		vmName:         options.vmName,
		limactlCommand: options.limactlCommand,
	}
	backend, err := ensureDevelopmentPackages(packageOptions, actualConfig)
	if err != nil {
		return err
	}
	if err := verifyDevelopmentExecutables(packageOptions, actualConfig, backend); err != nil {
		return err
	}
	if actualConfig.CopyGitConfig {
		prepareGitFunction := prepareGit
		if actualConfig.GitHub.Enabled {
			prepareGitFunction = prepareGitWithGitHub
		}
		if err := prepareGitFunction(options.project, options.vmName, options.limactlCommand, actualConfig.GPGForwarding); err != nil {
			return err
		}
	}
	environment, err = workflow.environment(options.project, options.vmName, options.limactlCommand, environment)
	if err != nil {
		return err
	}
	environment, err = options.github.environment(environment)
	if err != nil {
		return err
	}
	for _, setup := range actualConfig.Setup {
		slog.Info("running setup", "source", setup.Source, "command_index", setup.Index, "vm", options.vmName)
		processOptions := workflowProcessOptions(options.limactlCommand, workflow, options.github)
		if _, err := runGuest(options.project, options.vmName, []string{shellCommand, shellStrictFlag, shellCommandFlag, setup.Command}, guestExecution(processOptions, environment)); err != nil {
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
	lockErr := withAdvisoryLock(AdvisoryLockOptions{
		Project:       canonicalProject,
		VMName:        vmName,
		StateRoot:     options.StateRoot,
		LockDirectory: options.LockDirectory,
	}, func(string) error {
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

func InstallAgent(project, agentName string, update bool, options WorkflowOptions) (returnedInstance LimaInstance, returnErr error) {
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
	lockErr := withAdvisoryLock(AdvisoryLockOptions{
		Project:       canonicalProject,
		VMName:        vmName,
		StateRoot:     options.StateRoot,
		LockDirectory: options.LockDirectory,
	}, func(string) error {
		instance, prepareErr := options.prepareVMLocked(canonicalProject, vmName)
		if prepareErr != nil {
			return prepareErr
		}
		if !configuredAgents[agentName] {
			if _, installErr := installAgentLocked(agentInstallationOptions{
				project:        canonicalProject,
				vmName:         vmName,
				agent:          agent,
				update:         update,
				limactlCommand: options.limaCommand(),
				contexts:       workflowContexts(options.gpg),
			}); installErr != nil {
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

func orderedAgentEnvironment(stateRoot, agentName string) ([][2]string, error) {
	values, err := agentStateEnvironment(stateRoot, agentName)
	if err != nil {
		return nil, err
	}
	var order []string
	switch agentName {
	case codexAgentName:
		order = []string{codeXHomeEnvironment}
	case claudeAgentName:
		order = []string{claudeConfigEnvironment}
	default:
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

func agentWrapperContent(stateRoot, agentName, executablePath string) (string, error) {
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
		shellStrictScript,
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

func AgentWrapperContent(stateRoot, agentName, executablePath string) (string, error) {
	return agentWrapperContent(stateRoot, agentName, executablePath)
}

func agentWrapperSetupScript(stateRoot, vmName string, executables []AgentExecutable) (string, error) {
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
		shellStrictScript,
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
			temporaryPathAssignment,
		)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func PrepareAgentWrappers(wrapperOptions AgentWrapperOptions) error {
	script, err := agentWrapperSetupScript(wrapperOptions.StateRoot, wrapperOptions.VMName, wrapperOptions.Executables)
	if err != nil {
		return err
	}
	processOptions := defaultProcessOptions(wrapperOptions.LimaCommand)
	_, err = runGuest(wrapperOptions.Project, wrapperOptions.VMName, []string{shellCommand, shellCommandFlag, script}, guestExecution(processOptions, nil))
	return err
}

func prepareAgentNamesLocked(options agentPreparationOptions, agentNames []string) error {
	stateRoot := options.stateRoot
	if stateRoot == "" {
		stateRoot = options.project
	}
	executables := make([]AgentExecutable, 0, len(agentNames))
	codexRequested := false
	for _, agentName := range agentNames {
		agent, err := agentSpec(agentName)
		if err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", agentName, options.vmName, err)
		}
		executablePath, err := installAgentLocked(agentInstallationOptions{
			project:        options.project,
			vmName:         options.vmName,
			agent:          agent,
			update:         agentName == options.updateAgentName,
			limactlCommand: options.limactlCommand,
			contexts:       options.contexts,
		})
		if err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", agentName, options.vmName, err)
		}
		if _, err := EnsureAgentStateDirectories(stateRoot, agentName); err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", agentName, options.vmName, err)
		}
		if _, err := RefreshAgentInstructions(stateRoot, agentName, nil); err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", agentName, options.vmName, err)
		}
		if agentName == codexAgentName {
			codexRequested = true
		}
		executables = append(executables, AgentExecutable{Name: agentName, Path: executablePath})
	}
	if codexRequested && len(options.trustDirectories) != 0 {
		if err := ensureCodexDirectoryTrust(stateRoot, options.trustDirectories, options.lockDirectory); err != nil {
			return ljaError("cannot prepare agent %s in VM %s: %w", codexAgentName, options.vmName, err)
		}
	}
	if options.prepareWrappers && len(executables) != 0 {
		if err := PrepareAgentWrappers(AgentWrapperOptions{
			Project:     options.project,
			VMName:      options.vmName,
			StateRoot:   stateRoot,
			Executables: executables,
			LimaCommand: options.limactlCommand,
		}); err != nil {
			return ljaError("cannot prepare agent wrappers in VM %s: %w", options.vmName, err)
		}
	}
	return nil
}

func prepareConfiguredAgentsLocked(options agentPreparationOptions) error {
	agentNames, err := configuredAgentNames(options.development)
	if err != nil {
		return err
	}
	return prepareAgentNamesLocked(options, agentNames)
}

func prepareAgents(preparation AgentPreparationOptions) (returnedInstance LimaInstance, returnErr error) {
	options := preparation.Workflow
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

	canonicalProject, err := canonicalProjectPath(preparation.Project)
	if err != nil {
		return LimaInstance{}, err
	}
	agentNames, err := effectiveAgentNames(preparation.SelectedAgent, preparation.WithAgents, options.Development)
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
	trustDirectories := preparation.TrustDirectories
	if trustDirectories == nil {
		trustDirectories = []string{canonicalProject}
	}
	preparationConfig := developmentConfigOrDefault(options.Development)
	preparationConfig.Agents = []string{}
	options.Development = &preparationConfig
	options.agentTrustDirectories = append([]string{}, trustDirectories...)
	returnValue := LimaInstance{}
	lockErr := withAdvisoryLock(AdvisoryLockOptions{
		Project:       canonicalProject,
		VMName:        vmName,
		StateRoot:     options.StateRoot,
		LockDirectory: options.LockDirectory,
	}, func(string) error {
		instance, prepareErr := options.prepareVMLocked(canonicalProject, vmName)
		if prepareErr != nil {
			return prepareErr
		}
		if err := prepareAgentNamesLocked(options.agentPreparation(canonicalProject, vmName, len(agentNames) > 1, ""), agentNames); err != nil {
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

func PrepareAgents(preparation AgentPreparationOptions) (LimaInstance, error) {
	return prepareAgents(preparation)
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

func hasOption(arguments, options []string) bool {
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

func RunAgent(project string, runOptions AgentRunOptions) (exitCode int, returnErr error) {
	agentName := runOptions.AgentName
	arguments := runOptions.Arguments
	withAgents := runOptions.WithAgents
	workingDirectory := runOptions.WorkingDirectory
	options := runOptions.Workflow
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
	instance, err := prepareAgents(AgentPreparationOptions{
		Project:          canonicalProject,
		SelectedAgent:    agentName,
		WithAgents:       withAgents,
		TrustDirectories: trustDirectories,
		Workflow:         options,
	})
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
	maps.Copy(environment, options.Environment)
	maps.Copy(environment, stateEnvironment)
	maps.Copy(environment, agentEnvironment)
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
	result, err := runGuest(workingDirectory, instance.Name, guestArguments, guestExecution(optionsForGuest, environment))
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
	limaArguments := []string{limaShellOperation, limaWorkdirFlag, workingDirectory, instance.Name}
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
