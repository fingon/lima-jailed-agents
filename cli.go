package lja

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/alecthomas/kong"
)

const (
	createCommandName   = "create"
	stopCommandName     = "stop"
	deleteCommandName   = "delete"
	configCommandName   = "config"
	statusCommandName   = "status"
	updateCommandName   = "update"
	recreateCommandName = "recreate"
	helpCommandName     = "help"
	helpFlag            = "--help"
	recreateFlag        = "--recreate"
)

type passthroughCommand struct {
	Arguments []string `arg:"" help:"arguments forwarded to the guest command" optional:"" passthrough:"all"`
}

type lifecycleCommand struct {
	All bool `help:"apply to all LJA VMs" name:"all" short:"a"`
}

type updateCommand struct {
	Agent string `arg:"" enum:"codex,claude,opencode" help:"agent to update"`
}

type CLI struct {
	Recreate     bool     `help:"replace the VM after preparing a new one (create, shell, make, agents, update)" name:"recreate"`
	Project      string   `help:"select an exact project directory" name:"project" placeholder:"PATH" short:""`
	Verbose      bool     `help:"enable verbose diagnostic logging" name:"verbose" short:"v"`
	WithAgent    []string `enum:"codex,claude,opencode" help:"prepare an additional agent for an agent launch" name:"with-agent" sep:"none"`
	StateDir     *string  `help:"shared agent state root" name:"state-dir" placeholder:"PATH" xor:"storage"`
	ProjectState bool     `help:"keep agent state in the project" name:"project-state" xor:"storage"`

	Codex    passthroughCommand `cmd:"" help:"launch codex" optional:""`
	Claude   passthroughCommand `cmd:"" help:"launch claude" optional:""`
	Opencode passthroughCommand `cmd:"" help:"launch opencode" name:"opencode" optional:""`
	Shell    passthroughCommand `cmd:"" help:"open a shell in the project VM" optional:""`
	Make     passthroughCommand `cmd:"" help:"run make in the project VM" optional:""`
	Create   struct{}           `cmd:"" help:"prepare the project VM only if absent" optional:""`
	Config   struct{}           `cmd:"" help:"show resolved development configuration" optional:""`
	Status   struct{}           `cmd:"" help:"show project VM status" optional:""`
	Stop     lifecycleCommand   `cmd:"" help:"stop the project VM or all LJA VMs" optional:""`
	Delete   lifecycleCommand   `cmd:"" help:"force-delete the project VM or all LJA VMs" optional:""`
	Update   updateCommand      `cmd:"" help:"update one installed agent" optional:""`
	Help     struct{}           `cmd:"" default:"1" help:"show help" hidden:""`
}

func newParser(cli *CLI) (*kong.Kong, error) {
	return kong.New(cli,
		kong.Name(programName),
		kong.Description("Run coding agents and project commands in a project-specific Lima VM."),
	)
}

func forwardedArguments(arguments []string) []string {
	forwarded := append([]string{}, arguments...)
	if len(forwarded) > 0 && forwarded[0] == "--" {
		return forwarded[1:]
	}
	return forwarded
}

func commandName(context *kong.Context) string {
	command := context.Command()
	if before, _, ok := strings.Cut(command, " "); ok {
		return before
	}
	return command
}

func requestedAgents(cli *CLI, command string) ([]string, error) {
	if cli.Recreate && command != createCommandName && command != limaShellOperation && command != makeCommand && command != updateCommandName && !isAgentCommand(command) {
		return nil, ljaError("--recreate is only valid with create, shell, make, agent launch, and update commands")
	}
	if len(cli.WithAgent) != 0 && !isAgentCommand(command) {
		return nil, ljaError("--with-agent is only valid with agent launch commands")
	}
	if !isAgentCommand(command) {
		return []string{}, nil
	}
	return normalizeAgentNames(command, cli.WithAgent)
}

func isAgentCommand(command string) bool {
	switch command {
	case codexAgentName, claudeAgentName, openCodeAgentName:
		return true
	default:
		return false
	}
}

func configureLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	options := &slog.HandlerOptions{Level: level}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, options)))
}

func reportCLIMessage(message string) int {
	if _, err := fmt.Fprintf(os.Stderr, "%s: error: %s\n", programName, message); err != nil {
		slog.Error("cannot write CLI error", "error", err)
	}
	return 1
}

func reportCLIError(err error) int {
	return reportCLIMessage(err.Error())
}

func (cli *CLI) allVMs(command string) bool {
	return command == stopCommandName && cli.Stop.All || command == deleteCommandName && cli.Delete.All
}

func runCommand(cli *CLI, command, project, workingDirectory string) (int, error) {
	preparedAgents, err := requestedAgents(cli, command)
	if err != nil {
		return 0, err
	}
	if cli.allVMs(command) {
		if command == deleteCommandName {
			return 0, DeleteAllVMs(limaCtlCommand)
		}
		return 0, StopAllVMs(limaCtlCommand)
	}
	if project == "" {
		return 0, ljaError("%s requires a project", command)
	}
	if command == deleteCommandName {
		return 0, DeleteVM(project, limaCtlCommand)
	}
	environment := environmentMap()
	development, err := loadDevelopmentConfig(project, environment, "")
	if err != nil {
		return 0, err
	}
	if command == configCommandName {
		encoded, yamlErr := yamlConfiguration(development)
		if yamlErr != nil {
			return 0, yamlErr
		}
		if _, writeErr := os.Stdout.Write(encoded); writeErr != nil {
			return 0, ljaError("cannot write configuration: %w", writeErr)
		}
		return 0, nil
	}
	stateRoot, err := resolveStateRoot(project, cli.StateDir, cli.ProjectState, environment)
	if err != nil {
		return 0, err
	}
	if command == statusCommandName {
		if err := ShowStatus(project, stateRoot, limaCtlCommand); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if command == stopCommandName {
		if err := StopVM(project, stateRoot, limaCtlCommand, ""); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if command == createCommandName {
		instance, err := CreateVM(project, WorkflowOptions{StateRoot: stateRoot, Development: &development, hostEnvironment: environment, LimaCommand: limaCtlCommand, Recreate: cli.Recreate})
		if err != nil {
			return 0, err
		}
		if _, err := fmt.Printf("vm: %s\nstatus: %s\n", instance.Name, instance.Status); err != nil {
			return 0, ljaError("cannot report VM: %w", err)
		}
		return 0, nil
	}
	resolvedEnvironment, err := development.ResolveEnvironment(environment)
	if err != nil {
		return 0, err
	}
	workflowOptions := WorkflowOptions{
		Recreate:        cli.Recreate,
		StateRoot:       stateRoot,
		Development:     &development,
		Environment:     resolvedEnvironment,
		hostEnvironment: environment,
		LimaCommand:     limaCtlCommand,
	}
	switch command {
	case limaShellOperation:
		return OpenShell(project, forwardedArguments(cli.Shell.Arguments), workingDirectory, workflowOptions)
	case makeCommand:
		arguments := append([]string{makeCommand}, forwardedArguments(cli.Make.Arguments)...)
		return OpenShell(project, arguments, workingDirectory, workflowOptions)
	case updateCommandName:
		instance, updateErr := InstallAgent(project, cli.Update.Agent, true, workflowOptions)
		if updateErr != nil {
			return 0, updateErr
		}
		if _, err := fmt.Printf("updated: %s\nvm: %s\n", cli.Update.Agent, instance.Name); err != nil {
			return 0, ljaError("cannot report update: %w", err)
		}
		return 0, nil
	case codexAgentName, claudeAgentName, openCodeAgentName:
		arguments := []string{}
		switch command {
		case codexAgentName:
			arguments = forwardedArguments(cli.Codex.Arguments)
		case claudeAgentName:
			arguments = forwardedArguments(cli.Claude.Arguments)
		case openCodeAgentName:
			arguments = forwardedArguments(cli.Opencode.Arguments)
		}
		code, runErr := RunAgent(project, AgentRunOptions{
			AgentName:        command,
			Arguments:        arguments,
			WithAgents:       preparedAgents[1:],
			WorkingDirectory: workingDirectory,
			Workflow:         workflowOptions,
		})
		return code, runErr
	default:
		return 0, ljaError("%s is not implemented yet", command)
	}
}

func Main(arguments []string) int {
	cli := CLI{}
	parser, err := newParser(&cli)
	if err != nil {
		return reportCLIError(err)
	}
	context, err := parser.Parse(arguments)
	if err != nil {
		return reportCLIError(err)
	}
	command := commandName(context)
	if _, err := requestedAgents(&cli, command); err != nil {
		return reportCLIError(err)
	}
	if command == helpCommandName {
		if len(context.Path) > 1 {
			context.Path = context.Path[:1]
		}
	}
	if command == "" || command == helpCommandName {
		if usageErr := context.PrintUsage(false); usageErr != nil {
			return reportCLIMessage("cannot print help: " + usageErr.Error())
		}
		return 0
	}
	configureLogging(cli.Verbose)
	if cli.allVMs(command) {
		status, runErr := runCommand(&cli, command, "", "")
		if runErr != nil {
			return reportCLIError(runErr)
		}
		return status
	}
	currentDirectory, err := os.Getwd()
	if err != nil {
		return reportCLIMessage("cannot get current directory: " + err.Error())
	}
	project, err := resolveProject(cli.Project, currentDirectory)
	if err != nil {
		return reportCLIError(err)
	}
	workingDirectory := project
	if cli.Project == "" {
		project, err = discoverProject(workingDirectory, limaCtlCommand)
		if err != nil {
			return reportCLIError(err)
		}
	}
	status, runErr := runCommand(&cli, command, project, workingDirectory)
	if runErr != nil {
		return reportCLIError(runErr)
	}
	return status
}
