package lja

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/alecthomas/kong"
)

type passthroughCommand struct {
	Arguments []string `arg:"" optional:"" passthrough:"all" help:"arguments forwarded to the guest command"`
}

type updateCommand struct {
	Agent string `arg:"" enum:"codex,claude,opencode" help:"agent to update"`
}

type CLI struct {
	Project      string   `name:"project" short:"" placeholder:"PATH" help:"select an exact project directory"`
	Verbose      bool     `short:"v" name:"verbose" help:"enable verbose diagnostic logging"`
	WithAgent    []string `name:"with-agent" enum:"codex,claude,opencode" sep:"none" help:"prepare an additional agent for an agent launch"`
	StateDir     *string  `name:"state-dir" placeholder:"PATH" xor:"storage" help:"shared agent state root"`
	ProjectState bool     `name:"project-state" xor:"storage" help:"keep agent state in the project"`

	Codex    passthroughCommand `cmd:"" optional:"" help:"launch codex"`
	Claude   passthroughCommand `cmd:"" optional:"" help:"launch claude"`
	Opencode passthroughCommand `cmd:"" optional:"" name:"opencode" help:"launch opencode"`
	Shell    passthroughCommand `cmd:"" optional:"" help:"open a shell in the project VM"`
	Config   struct{}           `cmd:"" optional:"" help:"show resolved development configuration"`
	Status   struct{}           `cmd:"" optional:"" help:"show project VM status"`
	Stop     struct{}           `cmd:"" optional:"" help:"stop the project VM"`
	StopAll  struct{}           `cmd:"" optional:"" name:"stop-all" help:"stop all LJA VMs"`
	Update   updateCommand      `cmd:"" optional:"" help:"update one installed agent"`
	Help     struct{}           `cmd:"" default:"1" hidden:"" help:"show help"`
}

func newParser(cli *CLI) (*kong.Kong, error) {
	return kong.New(cli,
		kong.Name(programName),
		kong.Description("Run coding agents in a project-specific Lima VM."),
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
	if index := strings.IndexByte(command, ' '); index >= 0 {
		return command[:index]
	}
	return command
}

func requestedAgents(cli *CLI, command string) ([]string, error) {
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

func runCommand(cli *CLI, command string, project string, workingDirectory string) (int, error) {
	preparedAgents, err := requestedAgents(cli, command)
	if err != nil {
		return 0, err
	}
	if command == "stop-all" {
		if err := StopAllVMs(limaCtlCommand); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if project == "" {
		return 0, ljaError("%s requires a project", command)
	}
	environment := environmentMap()
	development, err := loadDevelopmentConfig(project, environment, "")
	if err != nil {
		return 0, err
	}
	if command == "config" {
		encoded, jsonErr := jsonConfiguration(development)
		if jsonErr != nil {
			return 0, jsonErr
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
	if command == "status" {
		if err := ShowStatus(project, stateRoot, limaCtlCommand); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if command == "stop" {
		if err := StopVM(project, stateRoot, limaCtlCommand, ""); err != nil {
			return 0, err
		}
		return 0, nil
	}
	resolvedEnvironment, err := development.ResolveEnvironment(environment)
	if err != nil {
		return 0, err
	}
	workflowOptions := WorkflowOptions{
		StateRoot:   stateRoot,
		Development: &development,
		Environment: resolvedEnvironment,
		LimaCommand: limaCtlCommand,
	}
	switch command {
	case "shell":
		return OpenShell(project, forwardedArguments(cli.Shell.Arguments), workingDirectory, workflowOptions)
	case "update":
		instance, updateErr := InstallAgent(project, cli.Update.Agent, true, workflowOptions)
		if updateErr != nil {
			return 0, updateErr
		}
		fmt.Printf("updated: %s\nvm: %s\n", cli.Update.Agent, instance.Name)
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
		code, runErr := RunAgent(project, command, arguments, preparedAgents[1:], workingDirectory, workflowOptions)
		return code, runErr
	default:
		return 0, ljaError("%s is not implemented yet", command)
	}
}

func Main(arguments []string) int {
	cli := CLI{}
	parser, err := newParser(&cli)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", programName, err)
		return 1
	}
	context, err := parser.Parse(arguments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", programName, err)
		return 1
	}
	command := commandName(context)
	if _, err := requestedAgents(&cli, command); err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", programName, err)
		return 1
	}
	if command == "help" {
		if len(context.Path) > 1 {
			context.Path = context.Path[:1]
		}
	}
	if command == "" || command == "help" {
		if usageErr := context.PrintUsage(false); usageErr != nil {
			fmt.Fprintf(os.Stderr, "%s: error: cannot print help: %v\n", programName, usageErr)
			return 1
		}
		return 0
	}
	configureLogging(cli.Verbose)
	if command == "stop-all" {
		status, runErr := runCommand(&cli, command, "", "")
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "%s: error: %v\n", programName, runErr)
			return 1
		}
		return status
	}
	currentDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: cannot get current directory: %v\n", programName, err)
		return 1
	}
	project, err := resolveProject(cli.Project, currentDirectory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", programName, err)
		return 1
	}
	workingDirectory := project
	if cli.Project == "" {
		project, err = discoverProject(workingDirectory, limaCtlCommand)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: error: %v\n", programName, err)
			return 1
		}
	}
	status, runErr := runCommand(&cli, command, project, workingDirectory)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", programName, runErr)
		return 1
	}
	return status
}
