package lja

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
)

const (
	catCommand                    = "cat"
	dpkgQueryCommand              = "dpkg-query"
	rpmCommand                    = "rpm"
	dnfCommand                    = "dnf"
	osReleasePath                 = "/etc/os-release"
	ubuntuDebianPackageBackend    = "ubuntu/debian"
	fedoraPackageBackend          = "fedora"
	ubuntuOSID                    = "ubuntu"
	debianOSID                    = "debian"
	fedoraGPGPackage              = "gnupg2"
	dpkgShowFlag                  = "--show"
	nodePackageName               = "nodejs"
	packageQueryMissingExitStatus = 1
)

var (
	debianPackageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(?::[a-z0-9][a-z0-9-]*)?$`)
	rpmPackageNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]*$`)
)

type guestPackageBackend struct {
	name           string
	queryCommand   string
	installCommand string
	requiredTools  []string
	gitPackage     string
	ghPackage      string
	gpgPackage     string
	nodePackages   []string
}

type guestPackageOptions struct {
	project        string
	vmName         string
	limactlCommand string
	contexts       []context.Context
}

type guestOSRelease struct {
	ID string
}

func parseOSReleaseValue(value string) (string, error) {
	if len(value) < 2 {
		return value, nil
	}
	if value[0] != '"' && value[0] != '\'' {
		return value, nil
	}
	if value[0] != value[len(value)-1] {
		return "", ljaError("invalid /etc/os-release quoting")
	}
	switch value[0] {
	case '"':
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", ljaError("invalid /etc/os-release quoted value: %w", err)
		}
		return decoded, nil
	case '\'':
		return value[1 : len(value)-1], nil
	default:
		return "", ljaError("invalid /etc/os-release quoting")
	}
}

func parseGuestOSRelease(content []byte) (guestOSRelease, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || name == "" || strings.TrimSpace(name) != name {
			return guestOSRelease{}, ljaError("invalid /etc/os-release entry")
		}
		decoded, err := parseOSReleaseValue(value)
		if err != nil {
			return guestOSRelease{}, err
		}
		values[name] = decoded
	}
	if err := scanner.Err(); err != nil {
		return guestOSRelease{}, ljaError("cannot read /etc/os-release: %w", err)
	}
	if values["ID"] == "" {
		return guestOSRelease{}, ljaError("/etc/os-release has no ID")
	}
	return guestOSRelease{ID: strings.ToLower(values["ID"])}, nil
}

func packageBackendForOSRelease(osRelease guestOSRelease) (guestPackageBackend, error) {
	id := strings.ToLower(osRelease.ID)
	switch id {
	case ubuntuOSID, debianOSID:
		return guestPackageBackend{
			name:           ubuntuDebianPackageBackend,
			queryCommand:   dpkgQueryCommand,
			installCommand: aptGetCommand,
			requiredTools:  []string{dpkgQueryCommand, aptGetCommand, sudoCommand},
			gitPackage:     gitCommand,
			ghPackage:      ghCommand,
			gpgPackage:     gpgPackage,
			nodePackages:   []string{nodePackageName, npmCommand},
		}, nil
	case "fedora":
		return guestPackageBackend{
			name:           fedoraPackageBackend,
			queryCommand:   rpmCommand,
			installCommand: dnfCommand,
			requiredTools:  []string{rpmCommand, dnfCommand, sudoCommand},
			gitPackage:     gitCommand,
			ghPackage:      ghCommand,
			gpgPackage:     fedoraGPGPackage,
			nodePackages:   []string{nodePackageName, npmCommand},
		}, nil
	default:
		return guestPackageBackend{}, ljaError("unsupported guest distribution %s; supported package backends are %s and %s", id, ubuntuDebianPackageBackend, fedoraPackageBackend)
	}
}

func readGuestOSRelease(project, vmName string, options processOptions) (guestOSRelease, error) {
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{catCommand, osReleasePath}, guestExecution(options, nil))
	if err != nil {
		return guestOSRelease{}, ljaError("cannot read %s in VM %s: %w", osReleasePath, vmName, err)
	}
	if result.ExitCode != 0 {
		if connectionErr := verifyGuestConnection(project, vmName, options); connectionErr != nil {
			return guestOSRelease{}, ljaError("cannot read %s in VM %s: %w", osReleasePath, vmName, connectionErr)
		}
		detail := processOutput(result)
		if detail != "" {
			return guestOSRelease{}, ljaError("cannot read %s in VM %s: exit status %d: %s", osReleasePath, vmName, result.ExitCode, detail)
		}
		return guestOSRelease{}, ljaError("cannot read %s in VM %s: exit status %d", osReleasePath, vmName, result.ExitCode)
	}
	osRelease, err := parseGuestOSRelease(result.Stdout)
	if err != nil {
		return guestOSRelease{}, ljaError("cannot parse %s in VM %s: %w", osReleasePath, vmName, err)
	}
	return osRelease, nil
}

func packageBackendForGuest(project, vmName string, options processOptions) (guestPackageBackend, error) {
	osRelease, err := readGuestOSRelease(project, vmName, options)
	if err != nil {
		return guestPackageBackend{}, err
	}
	backend, err := packageBackendForOSRelease(osRelease)
	if err != nil {
		return guestPackageBackend{}, ljaError("cannot select package backend for VM %s: %w", vmName, err)
	}
	for _, tool := range backend.requiredTools {
		available, probeErr := guestExecutableAvailable(project, vmName, tool, options)
		if probeErr != nil {
			return guestPackageBackend{}, ljaError("cannot verify %s package backend tool %s in VM %s: %w", backend.name, tool, vmName, probeErr)
		}
		if !available {
			return guestPackageBackend{}, ljaError("package backend %s in VM %s requires guest tool %s", backend.name, vmName, tool)
		}
	}
	return backend, nil
}

func (backend guestPackageBackend) queryArguments(packageName string) []string {
	switch backend.name {
	case fedoraPackageBackend:
		return []string{backend.queryCommand, "-q", "--whatprovides", packageName}
	default:
		return []string{backend.queryCommand, dpkgShowFlag, "--showformat=${Status}", packageName}
	}
}

func (backend guestPackageBackend) validatePackageName(packageName string) error {
	pattern := debianPackageNamePattern
	if backend.name == fedoraPackageBackend {
		pattern = rpmPackageNamePattern
	}
	if !pattern.MatchString(packageName) {
		return ljaError("invalid package name %s for %s backend", packageName, backend.name)
	}
	return nil
}

func (backend guestPackageBackend) packageIsMissing(result ProcessResult) bool {
	if result.ExitCode != packageQueryMissingExitStatus {
		return false
	}
	detail := strings.ToLower(strings.TrimSpace(processOutput(result)))
	if detail == "" {
		return true
	}
	switch backend.name {
	case fedoraPackageBackend:
		return strings.Contains(detail, "no package provides") || strings.Contains(detail, "is not installed")
	default:
		return strings.Contains(detail, "no packages found matching") || strings.Contains(detail, "is not installed")
	}
}

func (backend guestPackageBackend) queryPackage(options guestPackageOptions, packageName string) (bool, error) {
	if err := backend.validatePackageName(packageName); err != nil {
		return false, ljaError("cannot query dependency %s with %s backend in VM %s: %w", packageName, backend.name, options.vmName, err)
	}
	processOptions := defaultProcessOptions(options.limactlCommand, options.contexts...)
	processOptions.captureOutput = true
	processOptions.check = false
	result, err := runGuest(options.project, options.vmName, backend.queryArguments(packageName), guestExecution(processOptions, nil))
	if err != nil {
		return false, ljaError("cannot query dependency %s with %s backend in VM %s: %w", packageName, backend.name, options.vmName, err)
	}
	if result.ExitCode == 0 {
		if backend.name == ubuntuDebianPackageBackend {
			return strings.TrimSpace(string(result.Stdout)) == packageInstalledStatus, nil
		}
		return true, nil
	}
	if backend.packageIsMissing(result) {
		if connectionErr := verifyGuestConnection(options.project, options.vmName, processOptions); connectionErr != nil {
			return false, ljaError("cannot query dependency %s with %s backend in VM %s: %w", packageName, backend.name, options.vmName, connectionErr)
		}
		return false, nil
	}
	if connectionErr := verifyGuestConnection(options.project, options.vmName, processOptions); connectionErr != nil {
		return false, ljaError("cannot query dependency %s with %s backend in VM %s: %w", packageName, backend.name, options.vmName, connectionErr)
	}
	detail := processOutput(result)
	if detail != "" {
		return false, ljaError("package database query failed for dependency %s with %s backend in VM %s: exit status %d: %s", packageName, backend.name, options.vmName, result.ExitCode, detail)
	}
	return false, ljaError("package database query failed for dependency %s with %s backend in VM %s: exit status %d", packageName, backend.name, options.vmName, result.ExitCode)
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

func (backend guestPackageBackend) installArguments(packageNames []string) []string {
	if backend.name == ubuntuDebianPackageBackend {
		return []string{shellCommand, shellStrictFlag, shellCommandFlag, guestPackageInstallationScript(packageNames)}
	}
	arguments := []string{sudoCommand, backend.installCommand, installCommand, "-y"}
	return append(arguments, packageNames...)
}

func (backend guestPackageBackend) installPackages(options guestPackageOptions, packageNames []string) error {
	for _, packageName := range packageNames {
		if err := backend.validatePackageName(packageName); err != nil {
			return ljaError("cannot install dependency %s with %s backend in VM %s: %w", packageName, backend.name, options.vmName, err)
		}
	}
	processOptions := defaultProcessOptions(options.limactlCommand, options.contexts...)
	if _, err := runGuest(options.project, options.vmName, backend.installArguments(packageNames), guestExecution(processOptions, nil)); err != nil {
		return ljaError("cannot install dependencies %s with %s backend in VM %s: %w", strings.Join(packageNames, ", "), backend.name, options.vmName, err)
	}
	return nil
}

func (backend guestPackageBackend) developmentPackageNames(config DevelopmentConfig) []string {
	packages := append([]string{}, config.Packages...)
	if config.GPGForwarding {
		packages = append(packages, backend.gpgPackage)
	}
	if config.CopyGitConfig {
		packages = append(packages, backend.gitPackage)
	}
	if config.GitHub.Enabled {
		packages = append(packages, backend.gitPackage, backend.ghPackage)
	}
	return uniqueStrings(packages)
}

func (backend guestPackageBackend) nodePackageNames() []string {
	return append([]string{}, backend.nodePackages...)
}

func ensureGuestPackagesWithBackend(options guestPackageOptions, packageNames []string, backend guestPackageBackend) error {
	missing := make([]string, 0, len(packageNames))
	for _, packageName := range packageNames {
		installed, queryErr := backend.queryPackage(options, packageName)
		if queryErr != nil {
			return queryErr
		}
		if !installed {
			missing = append(missing, packageName)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	slog.Info("installing packages", "backend", backend.name, "packages", missing, "vm", options.vmName)
	if err := backend.installPackages(options, missing); err != nil {
		return err
	}
	for _, packageName := range missing {
		installed, queryErr := backend.queryPackage(options, packageName)
		if queryErr != nil {
			return queryErr
		}
		if !installed {
			return ljaError("cannot verify dependency %s with %s backend in VM %s: installation did not provide the requested package", packageName, backend.name, options.vmName)
		}
	}
	return nil
}

func ensureDevelopmentPackages(options guestPackageOptions, config DevelopmentConfig) (guestPackageBackend, error) {
	if len(config.Packages) == 0 && !config.GPGForwarding && !config.CopyGitConfig && !config.GitHub.Enabled {
		return guestPackageBackend{}, nil
	}
	guestOptions := defaultProcessOptions(options.limactlCommand, options.contexts...)
	backend, err := packageBackendForGuest(options.project, options.vmName, guestOptions)
	if err != nil {
		return guestPackageBackend{}, ljaError("cannot prepare dependencies in VM %s: %w", options.vmName, err)
	}
	packageNames := backend.developmentPackageNames(config)
	if err := ensureGuestPackagesWithBackend(options, packageNames, backend); err != nil {
		return guestPackageBackend{}, err
	}
	return backend, nil
}

func verifyDevelopmentExecutables(options guestPackageOptions, config DevelopmentConfig, backend guestPackageBackend) error {
	executables := make([]string, 0, 5)
	if config.CopyGitConfig {
		executables = append(executables, gitCommand)
	}
	if config.GitHub.Enabled {
		executables = append(executables, gitCommand, ghCommand)
	}
	if config.GPGForwarding {
		executables = append(executables, gpgCommand, gpgConfCommand)
	}
	executables = uniqueStrings(executables)
	guestOptions := defaultProcessOptions(options.limactlCommand, options.contexts...)
	for _, executable := range executables {
		available, err := guestExecutableAvailable(options.project, options.vmName, executable, guestOptions)
		if err != nil {
			return ljaError("cannot verify required executable %s with %s backend in VM %s: %w", executable, backend.name, options.vmName, err)
		}
		if !available {
			return ljaError("required executable %s is missing with %s backend in VM %s", executable, backend.name, options.vmName)
		}
	}
	return nil
}
