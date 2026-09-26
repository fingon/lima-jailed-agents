package lja

import (
	"log/slog"
	"slices"
)

const (
	pipxCommand    = "pipx"
	toolSubcommand = "tool"
)

type logicalToolDefinition struct {
	name             string
	executable       string
	nativePackages   []string
	prerequisites    []string
	installArguments []string
}

type logicalToolPlan struct {
	nativePackages []string
	tools          []logicalToolDefinition
}

func logicalToolDefinitionByName(name string) (logicalToolDefinition, error) {
	switch name {
	case logicalToolUV:
		return logicalToolDefinition{
			name:             name,
			executable:       logicalToolUV,
			nativePackages:   []string{pipxCommand},
			installArguments: []string{pipxCommand, installCommand, logicalToolUV},
		}, nil
	case logicalToolPrek:
		return logicalToolDefinition{
			name:             name,
			executable:       logicalToolPrek,
			prerequisites:    []string{logicalToolUV},
			installArguments: []string{logicalToolUV, toolSubcommand, installCommand, logicalToolPrek},
		}, nil
	default:
		return logicalToolDefinition{}, ljaError("unknown logical tool %s; expected one of %s, %s", name, logicalToolUV, logicalToolPrek)
	}
}

func resolveLogicalTools(requested []string) (logicalToolPlan, error) {
	plan := logicalToolPlan{
		nativePackages: []string{},
		tools:          []logicalToolDefinition{},
	}
	resolving := make(map[string]bool, len(requested))
	resolved := make(map[string]bool, len(requested))
	var visit func(string) error
	visit = func(name string) error {
		if resolved[name] {
			return nil
		}
		if resolving[name] {
			return ljaError("logical tool dependency cycle includes %s", name)
		}
		definition, err := logicalToolDefinitionByName(name)
		if err != nil {
			return err
		}
		resolving[name] = true
		for _, prerequisite := range definition.prerequisites {
			if err := visit(prerequisite); err != nil {
				return err
			}
		}
		for _, packageName := range definition.nativePackages {
			plan.nativePackages = appendUniqueString(plan.nativePackages, packageName)
		}
		plan.tools = append(plan.tools, definition)
		delete(resolving, name)
		resolved[name] = true
		return nil
	}
	for _, name := range requested {
		if err := visit(name); err != nil {
			return logicalToolPlan{}, err
		}
	}
	return plan, nil
}

func ensureLogicalTools(options guestPackageOptions, requested []string) error {
	plan, err := resolveLogicalTools(requested)
	if err != nil {
		return err
	}
	processOptions := defaultProcessOptions(options.limactlCommand, options.contexts...)
	for _, packageName := range plan.nativePackages {
		executable, executableErr := nativeToolExecutable(packageName)
		if executableErr != nil {
			return executableErr
		}
		path, _, probeErr := guestExecutableVersion(options.project, options.vmName, executable, processOptions)
		if probeErr != nil {
			return ljaError("cannot verify native logical tool dependency %s in VM %s: %w", packageName, options.vmName, probeErr)
		}
		if path == "" {
			return ljaError("native logical tool dependency %s is missing in VM %s", packageName, options.vmName)
		}
	}
	for _, definition := range plan.tools {
		path, _, probeErr := guestExecutableVersion(options.project, options.vmName, definition.executable, processOptions)
		if probeErr != nil {
			return ljaError("cannot verify logical tool %s in VM %s: %w", definition.name, options.vmName, probeErr)
		}
		if path != "" {
			slog.Debug("reusing installed logical tool", "tool", definition.name, "vm", options.vmName)
			continue
		}
		if err := installLogicalTool(options, definition); err != nil {
			return err
		}
		path, _, probeErr = guestExecutableVersion(options.project, options.vmName, definition.executable, processOptions)
		if probeErr != nil {
			return ljaError("cannot verify logical tool %s in VM %s after installation: %w", definition.name, options.vmName, probeErr)
		}
		if path == "" {
			return ljaError("logical tool %s was installed but executable %s is missing in VM %s", definition.name, definition.executable, options.vmName)
		}
	}
	return nil
}

func nativeToolExecutable(packageName string) (string, error) {
	switch packageName {
	case pipxCommand:
		return pipxCommand, nil
	default:
		return "", ljaError("native logical tool dependency %s has no executable definition", packageName)
	}
}

func installLogicalTool(options guestPackageOptions, definition logicalToolDefinition) error {
	slog.Info("installing logical tool", "tool", definition.name, "vm", options.vmName)
	processOptions := defaultProcessOptions(options.limactlCommand, options.contexts...)
	processOptions.captureOutput = true
	processOptions.check = false
	result, err := runGuest(options.project, options.vmName, definition.installArguments, guestExecution(processOptions, nil))
	if err != nil {
		return ljaError("cannot install logical tool %s in VM %s: %w", definition.name, options.vmName, err)
	}
	if result.ExitCode == 0 {
		return nil
	}
	if connectionErr := verifyGuestConnection(options.project, options.vmName, processOptions); connectionErr != nil {
		return ljaError("cannot install logical tool %s in VM %s because the guest connection failed: %w", definition.name, options.vmName, connectionErr)
	}
	detail := processOutput(result)
	if detail != "" {
		return ljaError("cannot install logical tool %s in VM %s: exit status %d: %s", definition.name, options.vmName, result.ExitCode, detail)
	}
	return ljaError("cannot install logical tool %s in VM %s: exit status %d", definition.name, options.vmName, result.ExitCode)
}

func appendUniqueString(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
