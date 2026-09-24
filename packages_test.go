package lja

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestParseGuestOSRelease(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		wantID  string
		want    string
	}{
		{name: "ubuntu", content: "NAME=Ubuntu\nID=ubuntu\n", wantID: "ubuntu"},
		{name: "quoted Fedora", content: "NAME=Fedora\nID=\"Fedora\"\n", wantID: "fedora"},
		{name: "comments and single quotes", content: "# comment\nID='debian'\n", wantID: "debian"},
		{name: "missing ID", content: "NAME=Unknown\n", want: "has no ID"},
		{name: "malformed entry", content: "ID ubuntu\n", want: "invalid /etc/os-release entry"},
		{name: "unterminated quote", content: "ID=\"ubuntu\n", want: "invalid /etc/os-release quoting"},
	} {
		t.Run(test.name, func(t *testing.T) {
			osRelease, err := parseGuestOSRelease([]byte(test.content))
			if test.want == "" {
				assert.NilError(t, err)
				assert.Equal(t, osRelease.ID, test.wantID)
			} else {
				assert.ErrorContains(t, err, test.want)
			}
		})
	}
}

func TestPackageBackendSelection(t *testing.T) {
	for _, test := range []struct {
		name            string
		id              string
		wantName        string
		wantQuery       []string
		wantInstall     []string
		wantGPG         string
		wantNode        []string
		wantDevelopment []string
		wantToolCount   int
	}{
		{
			name:            "Ubuntu",
			id:              "ubuntu",
			wantName:        ubuntuDebianPackageBackend,
			wantQuery:       []string{dpkgQueryCommand, "--show", "--showformat=${Status}", "make"},
			wantInstall:     []string{shellCommand, "-eu", shellCommandFlag, "sudo apt-get update && sudo apt-get install -y 'make' 'gcc'"},
			wantGPG:         gpgPackage,
			wantNode:        []string{"nodejs", npmCommand},
			wantDevelopment: []string{"make", "git", gpgPackage},
			wantToolCount:   3,
		},
		{
			name:            "Debian",
			id:              "debian",
			wantName:        ubuntuDebianPackageBackend,
			wantQuery:       []string{dpkgQueryCommand, "--show", "--showformat=${Status}", "make"},
			wantInstall:     []string{shellCommand, "-eu", shellCommandFlag, "sudo apt-get update && sudo apt-get install -y 'make' 'gcc'"},
			wantGPG:         gpgPackage,
			wantNode:        []string{"nodejs", npmCommand},
			wantDevelopment: []string{"make", "git", gpgPackage},
			wantToolCount:   3,
		},
		{
			name:            "Fedora capability query",
			id:              "fedora",
			wantName:        fedoraPackageBackend,
			wantQuery:       []string{rpmCommand, "-q", "--whatprovides", "make"},
			wantInstall:     []string{sudoCommand, dnfCommand, installCommand, "-y", "make", "gcc"},
			wantGPG:         "gnupg2",
			wantNode:        []string{"nodejs", npmCommand},
			wantDevelopment: []string{"make", "git", "gnupg2"},
			wantToolCount:   3,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend, err := packageBackendForOSRelease(guestOSRelease{ID: test.id})
			assert.NilError(t, err)
			assert.Equal(t, backend.name, test.wantName)
			assert.Equal(t, backend.gpgPackage, test.wantGPG)
			assert.DeepEqual(t, backend.nodePackageNames(), test.wantNode)
			assert.DeepEqual(t, backend.developmentPackageNames(DevelopmentConfig{
				Packages:      []string{"make", "git", "make"},
				GPGForwarding: true,
				CopyGitConfig: true,
			}), test.wantDevelopment)
			assert.DeepEqual(t, backend.queryArguments("make"), test.wantQuery)
			assert.DeepEqual(t, backend.installArguments([]string{"make", "gcc"}), test.wantInstall)
			assert.Equal(t, len(backend.requiredTools), test.wantToolCount)
		})
	}
	_, err := packageBackendForOSRelease(guestOSRelease{ID: "arch"})
	assert.ErrorContains(t, err, "supported package backends")
}

func TestPackageQueryMissingClassification(t *testing.T) {
	for _, test := range []struct {
		name    string
		backend guestPackageBackend
		result  ProcessResult
		want    bool
	}{
		{name: "Debian missing", backend: guestPackageBackend{name: ubuntuDebianPackageBackend}, result: ProcessResult{ExitCode: 1, Stderr: []byte("dpkg-query: no packages found matching make")}, want: true},
		{name: "Fedora capability missing", backend: guestPackageBackend{name: fedoraPackageBackend}, result: ProcessResult{ExitCode: 1, Stderr: []byte("no package provides make")}, want: true},
		{name: "database failure", backend: guestPackageBackend{name: fedoraPackageBackend}, result: ProcessResult{ExitCode: 1, Stderr: []byte("rpmdb: BDB0113 Thread/process failed")}},
		{name: "unexpected exit", backend: guestPackageBackend{name: ubuntuDebianPackageBackend}, result: ProcessResult{ExitCode: 2}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.backend.packageIsMissing(test.result), test.want)
		})
	}
}

func TestPackageNameValidation(t *testing.T) {
	assert.NilError(t, ValidateDevelopmentConfig([]byte("packages: [NodeJS_22-devel, libssl3:amd64]"), true))
	for _, test := range []struct {
		name    string
		backend guestPackageBackend
		values  []string
		valid   bool
	}{
		{name: "Debian architecture qualifier", backend: guestPackageBackend{name: ubuntuDebianPackageBackend}, values: []string{"libssl3:amd64"}, valid: true},
		{name: "Debian uppercase", backend: guestPackageBackend{name: ubuntuDebianPackageBackend}, values: []string{"NodeJS"}},
		{name: "RPM uppercase and underscore", backend: guestPackageBackend{name: fedoraPackageBackend}, values: []string{"NodeJS_22-devel"}, valid: true},
		{name: "RPM architecture qualifier", backend: guestPackageBackend{name: fedoraPackageBackend}, values: []string{"nodejs:amd64"}},
		{name: "option", backend: guestPackageBackend{name: fedoraPackageBackend}, values: []string{"--help"}},
		{name: "path", backend: guestPackageBackend{name: fedoraPackageBackend}, values: []string{"./nodejs"}},
		{name: "URL", backend: guestPackageBackend{name: fedoraPackageBackend}, values: []string{"https://example.test/pkg"}},
		{name: "glob", backend: guestPackageBackend{name: fedoraPackageBackend}, values: []string{"node*"}},
		{name: "dependency expression", backend: guestPackageBackend{name: fedoraPackageBackend}, values: []string{"nodejs >= 22"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, value := range test.values {
				err := test.backend.validatePackageName(value)
				if test.valid {
					assert.NilError(t, err)
				} else {
					assert.ErrorContains(t, err, "invalid package name")
				}
			}
		})
	}
}

func TestFedoraPackagePreparationUsesCapabilitiesAndDNF(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	statePath := filepath.Join(root, "installed")
	logPath := filepath.Join(root, "operations")
	t.Setenv("LJA_TEST_PACKAGE_STATE", statePath)
	t.Setenv("LJA_TEST_PACKAGE_LOG", logPath)
	commandPath := filepath.Join(root, limaCtlCommand)
	command := `#!/bin/sh
set -eu
printf '%s\0' "$@" >> "$LJA_TEST_PACKAGE_LOG"
if [ "$1" != "shell" ]; then exit 99; fi
shift 4
shift 4
case "$1" in
cat)
    printf 'ID=fedora\n'
    ;;
command)
    printf '/usr/bin/%s\n' "$3"
    ;;
true)
    ;;
rpm)
    if [ -f "$LJA_TEST_PACKAGE_STATE" ]; then
        printf '%s-22.0\n' "$4"
        exit 0
    fi
    printf 'no package provides %s\n' "$4" >&2
    exit 1
    ;;
sudo)
    if [ "$2" != "dnf" ] || [ "$3" != "install" ] || [ "$4" != "-y" ]; then
        exit 98
    fi
    : > "$LJA_TEST_PACKAGE_STATE"
    ;;
*)
    exit 97
    ;;
esac
`
	assert.NilError(t, os.WriteFile(commandPath, []byte(command), 0o755))

	err := ensureGuestPackages(project, "fedora-vm", []string{"nodejs", "npm"}, commandPath)
	assert.NilError(t, err)
	_, err = os.Stat(statePath)
	assert.NilError(t, err)
	operations, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(operations), "rpm\x00-q\x00--whatprovides\x00nodejs"))
	assert.Assert(t, strings.Contains(string(operations), "sudo\x00dnf\x00install\x00-y\x00nodejs\x00npm"))
}
