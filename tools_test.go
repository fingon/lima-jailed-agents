package lja

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestResolveLogicalTools(t *testing.T) {
	tests := []struct {
		name        string
		requested   []string
		wantNative  []string
		wantTools   []string
		wantInstall [][]string
	}{
		{name: "empty", requested: []string{}, wantNative: []string{}, wantTools: []string{}, wantInstall: [][]string{}},
		{
			name:        "uv",
			requested:   []string{logicalToolUV},
			wantNative:  []string{pipxCommand},
			wantTools:   []string{logicalToolUV},
			wantInstall: [][]string{{pipxCommand, installCommand, logicalToolUV}},
		},
		{
			name:        "prek includes uv",
			requested:   []string{logicalToolPrek},
			wantNative:  []string{pipxCommand},
			wantTools:   []string{logicalToolUV, logicalToolPrek},
			wantInstall: [][]string{{pipxCommand, installCommand, logicalToolUV}, {logicalToolUV, toolSubcommand, installCommand, logicalToolPrek}},
		},
		{
			name:        "reversed selection",
			requested:   []string{logicalToolPrek, logicalToolUV},
			wantNative:  []string{pipxCommand},
			wantTools:   []string{logicalToolUV, logicalToolPrek},
			wantInstall: [][]string{{pipxCommand, installCommand, logicalToolUV}, {logicalToolUV, toolSubcommand, installCommand, logicalToolPrek}},
		},
		{
			name:        "duplicate selection",
			requested:   []string{logicalToolUV, logicalToolPrek, logicalToolUV},
			wantNative:  []string{pipxCommand},
			wantTools:   []string{logicalToolUV, logicalToolPrek},
			wantInstall: [][]string{{pipxCommand, installCommand, logicalToolUV}, {logicalToolUV, toolSubcommand, installCommand, logicalToolPrek}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := resolveLogicalTools(test.requested)
			assert.NilError(t, err)
			assert.DeepEqual(t, plan.nativePackages, test.wantNative)
			names := make([]string, 0, len(plan.tools))
			installArguments := make([][]string, 0, len(plan.tools))
			for _, tool := range plan.tools {
				names = append(names, tool.name)
				installArguments = append(installArguments, tool.installArguments)
			}
			assert.DeepEqual(t, names, test.wantTools)
			assert.DeepEqual(t, installArguments, test.wantInstall)
		})
	}
}

func TestResolveLogicalToolsRejectsUnknownName(t *testing.T) {
	_, err := resolveLogicalTools([]string{unknownLogicalToolName})
	assert.ErrorContains(t, err, "unknown logical tool")
}

func TestEnsureLogicalToolsInstallsInDependencyOrder(t *testing.T) {
	project, vmName, options := vmFixture(t, "")
	database, err := readVMDatabase()
	assert.NilError(t, err)
	database.PackageInstallationDone = true
	assert.NilError(t, database.save())

	assert.NilError(t, ensureLogicalTools(guestPackageOptions{
		project:        project,
		vmName:         vmName,
		limactlCommand: options.LimaCommand,
	}, []string{logicalToolPrek}))

	database, err = readVMDatabase()
	assert.NilError(t, err)
	installations := make([][]string, 0, 2)
	for _, operation := range database.Operations {
		guestArguments := unwrapGuestPathArguments(operation[4:])
		if len(guestArguments) == 3 && guestArguments[0] == pipxCommand && guestArguments[1] == installCommand {
			installations = append(installations, guestArguments)
		}
		if len(guestArguments) == 4 && guestArguments[0] == logicalToolUV && guestArguments[1] == toolSubcommand && guestArguments[2] == installCommand {
			installations = append(installations, guestArguments)
		}
	}
	assert.DeepEqual(t, installations, [][]string{
		{pipxCommand, installCommand, logicalToolUV},
		{logicalToolUV, toolSubcommand, installCommand, logicalToolPrek},
	})
}
