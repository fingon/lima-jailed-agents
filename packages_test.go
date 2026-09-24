package lja

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

const (
	packageTestStateEnv       = "LJA_TEST_PACKAGE_STATE"
	packageTestLogEnv         = "LJA_TEST_PACKAGE_LOG"
	packageTestIDEnv          = "LJA_TEST_PACKAGE_ID"
	packageTestModeEnv        = "LJA_TEST_PACKAGE_MODE"
	packageTestMissingToolEnv = "LJA_TEST_PACKAGE_MISSING_TOOL"
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

func TestGitHubDevelopmentDependencies(t *testing.T) {
	for _, test := range []struct {
		name            string
		copyGitConfig   bool
		wantDevelopment []string
	}{
		{name: "without Git copying", wantDevelopment: []string{"git", ghCommand}},
		{name: "with Git copying", copyGitConfig: true, wantDevelopment: []string{"git", ghCommand}},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend, err := packageBackendForOSRelease(guestOSRelease{ID: "ubuntu"})
			assert.NilError(t, err)
			assert.DeepEqual(t, backend.developmentPackageNames(DevelopmentConfig{
				GitHub:        GitHubConfig{Enabled: true},
				CopyGitConfig: test.copyGitConfig,
			}), test.wantDevelopment)
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

func TestExistingGuestUsesReportedDistributionForPreparation(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	assert.NilError(t, os.Mkdir(project, 0o755))
	statePath := filepath.Join(root, "installed")
	logPath := filepath.Join(root, "operations")
	t.Setenv(packageTestStateEnv, statePath)
	t.Setenv(packageTestLogEnv, logPath)
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

	backend, err := ensureDevelopmentPackages(project, "fedora-vm", DevelopmentConfig{
		Lima:     map[string]any{limaBaseKey: "template:ubuntu"},
		Packages: []string{"nodejs", "npm"},
	}, commandPath)
	assert.NilError(t, err)
	assert.Equal(t, backend.name, fedoraPackageBackend)
	_, err = os.Stat(statePath)
	assert.NilError(t, err)
	operations, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(operations), "rpm\x00-q\x00--whatprovides\x00nodejs"))
	assert.Assert(t, strings.Contains(string(operations), "sudo\x00dnf\x00install\x00-y\x00nodejs\x00npm"))
}

func TestGuestPackagePreparationAcrossBackends(t *testing.T) {
	command := `#!/bin/sh
set -eu
if [ "$1" != "shell" ]; then
    exit 99
fi
shift 4
shift 4
case "$1" in
true)
    if [ "${LJA_TEST_PACKAGE_MODE-}" = "connection-failure" ]; then
        exit 7
    fi
    ;;
cat)
    printf 'ID=%s\n' "$LJA_TEST_PACKAGE_ID"
    ;;
command)
    if [ "${LJA_TEST_PACKAGE_MISSING_TOOL-}" = "$3" ]; then
        exit 1
    fi
    printf '/usr/bin/%s\n' "$3"
    ;;
dpkg-query)
    printf 'query\n' >> "$LJA_TEST_PACKAGE_LOG"
    if [ "${LJA_TEST_PACKAGE_MODE-}" = "query-failure" ]; then
        printf 'dpkg database failure\n' >&2
        exit 2
    fi
    if [ -f "$LJA_TEST_PACKAGE_STATE" ]; then
        printf 'install ok installed'
        exit 0
    fi
    printf 'dpkg-query: no packages found matching %s\n' "$4" >&2
    exit 1
    ;;
rpm)
    printf 'query\n' >> "$LJA_TEST_PACKAGE_LOG"
    if [ "${LJA_TEST_PACKAGE_MODE-}" = "query-failure" ]; then
        printf 'rpm database failure\n' >&2
        exit 2
    fi
    if [ -f "$LJA_TEST_PACKAGE_STATE" ]; then
        printf '%s-22.0\n' "$4"
        exit 0
    fi
    printf 'no package provides %s\n' "$4" >&2
    exit 1
    ;;
sh)
    if [ "$2" != "-eu" ] || [ "$3" != "-c" ]; then
        exit 98
    fi
    printf 'install\n' >> "$LJA_TEST_PACKAGE_LOG"
    if [ "${LJA_TEST_PACKAGE_MODE-}" = "install-failure" ]; then
        printf 'apt installation failed\n' >&2
        exit 17
    fi
    if [ "${LJA_TEST_PACKAGE_MODE-}" != "verification-failure" ]; then
        : > "$LJA_TEST_PACKAGE_STATE"
    fi
    ;;
sudo)
    if [ "$2" != "dnf" ] || [ "$3" != "install" ] || [ "$4" != "-y" ]; then
        exit 98
    fi
    printf 'install\n' >> "$LJA_TEST_PACKAGE_LOG"
    if [ "${LJA_TEST_PACKAGE_MODE-}" = "install-failure" ]; then
        printf 'dnf installation failed\n' >&2
        exit 17
    fi
    if [ "${LJA_TEST_PACKAGE_MODE-}" != "verification-failure" ]; then
        : > "$LJA_TEST_PACKAGE_STATE"
    fi
    ;;
*)
    exit 97
    ;;
esac
`

	for _, test := range []struct {
		name             string
		id               string
		initiallyPresent bool
		mode             string
		missingTool      string
		wantBackend      string
		wantError        string
		wantInstall      bool
	}{
		{name: "Ubuntu installed", id: "ubuntu", initiallyPresent: true, wantBackend: ubuntuDebianPackageBackend},
		{name: "Ubuntu missing", id: "ubuntu", wantBackend: ubuntuDebianPackageBackend, wantInstall: true},
		{name: "Fedora installed capability", id: "fedora", initiallyPresent: true, wantBackend: fedoraPackageBackend},
		{name: "Fedora missing capability", id: "fedora", wantBackend: fedoraPackageBackend, wantInstall: true},
		{name: "Ubuntu missing tool", id: "ubuntu", missingTool: aptGetCommand, wantError: "requires guest tool apt-get"},
		{name: "Fedora missing tool", id: "fedora", missingTool: dnfCommand, wantError: "requires guest tool dnf"},
		{name: "Ubuntu query failure", id: "ubuntu", mode: "query-failure", wantError: "package database query failed"},
		{name: "Fedora query failure", id: "fedora", mode: "query-failure", wantError: "package database query failed"},
		{name: "Ubuntu connection failure", id: "ubuntu", mode: "connection-failure", wantError: "cannot connect to guest VM"},
		{name: "Fedora connection failure", id: "fedora", mode: "connection-failure", wantError: "cannot connect to guest VM"},
		{name: "Ubuntu install failure", id: "ubuntu", mode: "install-failure", wantError: "cannot install dependencies"},
		{name: "Fedora install failure", id: "fedora", mode: "install-failure", wantError: "cannot install dependencies"},
		{name: "Ubuntu post-install verification", id: "ubuntu", mode: "verification-failure", wantError: "installation did not provide"},
		{name: "Fedora post-install verification", id: "fedora", mode: "verification-failure", wantError: "installation did not provide"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "project")
			assert.NilError(t, os.Mkdir(project, 0o755))
			statePath := filepath.Join(root, "state")
			logPath := filepath.Join(root, "operations")
			commandPath := filepath.Join(root, limaCtlCommand)
			assert.NilError(t, os.WriteFile(commandPath, []byte(command), 0o755))
			if test.initiallyPresent {
				assert.NilError(t, os.WriteFile(statePath, nil, 0o600))
			}
			t.Setenv(packageTestStateEnv, statePath)
			t.Setenv(packageTestLogEnv, logPath)
			t.Setenv(packageTestIDEnv, test.id)
			t.Setenv(packageTestModeEnv, test.mode)
			t.Setenv(packageTestMissingToolEnv, test.missingTool)

			backend, err := ensureDevelopmentPackages(project, "package-test-vm", DevelopmentConfig{Packages: []string{"make"}}, commandPath)
			if test.wantError != "" {
				assert.ErrorContains(t, err, test.wantError)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, backend.name, test.wantBackend)
			operations, err := os.ReadFile(logPath)
			assert.NilError(t, err)
			assert.Equal(t, strings.Contains(string(operations), "install\n"), test.wantInstall)
		})
	}
}
