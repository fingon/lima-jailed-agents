package lja

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
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
	packageQueryMissingExitStatus = 1
)

type guestPackageBackend struct {
	name           string
	queryCommand   string
	installCommand string
	requiredTools  []string
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
	case "ubuntu", "debian":
		return guestPackageBackend{
			name:           ubuntuDebianPackageBackend,
			queryCommand:   dpkgQueryCommand,
			installCommand: aptGetCommand,
			requiredTools:  []string{dpkgQueryCommand, aptGetCommand, sudoCommand},
		}, nil
	case "fedora":
		return guestPackageBackend{
			name:           fedoraPackageBackend,
			queryCommand:   rpmCommand,
			installCommand: dnfCommand,
			requiredTools:  []string{rpmCommand, dnfCommand, sudoCommand},
		}, nil
	default:
		return guestPackageBackend{}, ljaError("unsupported guest distribution %s; supported package backends are %s and %s", id, ubuntuDebianPackageBackend, fedoraPackageBackend)
	}
}

func readGuestOSRelease(project string, vmName string, limactlCommand string, contexts ...context.Context) (guestOSRelease, error) {
	options := defaultProcessOptions(limactlCommand, contexts...)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, []string{catCommand, osReleasePath}, options, nil)
	if err != nil {
		return guestOSRelease{}, ljaError("cannot read %s in VM %s: %w", osReleasePath, vmName, err)
	}
	if result.ExitCode != 0 {
		if connectionErr := verifyGuestConnection(project, vmName, limactlCommand, contexts...); connectionErr != nil {
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

func packageBackendForGuest(project string, vmName string, limactlCommand string, contexts ...context.Context) (guestPackageBackend, error) {
	osRelease, err := readGuestOSRelease(project, vmName, limactlCommand, contexts...)
	if err != nil {
		return guestPackageBackend{}, err
	}
	backend, err := packageBackendForOSRelease(osRelease)
	if err != nil {
		return guestPackageBackend{}, ljaError("cannot select package backend for VM %s: %w", vmName, err)
	}
	for _, tool := range backend.requiredTools {
		available, probeErr := guestExecutableAvailable(project, vmName, tool, limactlCommand, contexts...)
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
		return []string{backend.queryCommand, "--show", "--showformat=${Status}", packageName}
	}
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

func (backend guestPackageBackend) queryPackage(project string, vmName string, packageName string, limactlCommand string, contexts ...context.Context) (bool, error) {
	options := defaultProcessOptions(limactlCommand, contexts...)
	options.captureOutput = true
	options.check = false
	result, err := runGuest(project, vmName, backend.queryArguments(packageName), options, nil)
	if err != nil {
		return false, ljaError("cannot query dependency %s with %s backend in VM %s: %w", packageName, backend.name, vmName, err)
	}
	if result.ExitCode == 0 {
		if backend.name == ubuntuDebianPackageBackend {
			return strings.TrimSpace(string(result.Stdout)) == packageInstalledStatus, nil
		}
		return true, nil
	}
	if backend.packageIsMissing(result) {
		if connectionErr := verifyGuestConnection(project, vmName, limactlCommand, contexts...); connectionErr != nil {
			return false, ljaError("cannot query dependency %s with %s backend in VM %s: %w", packageName, backend.name, vmName, connectionErr)
		}
		return false, nil
	}
	if connectionErr := verifyGuestConnection(project, vmName, limactlCommand, contexts...); connectionErr != nil {
		return false, ljaError("cannot query dependency %s with %s backend in VM %s: %w", packageName, backend.name, vmName, connectionErr)
	}
	detail := processOutput(result)
	if detail != "" {
		return false, ljaError("package database query failed for dependency %s with %s backend in VM %s: exit status %d: %s", packageName, backend.name, vmName, result.ExitCode, detail)
	}
	return false, ljaError("package database query failed for dependency %s with %s backend in VM %s: exit status %d", packageName, backend.name, vmName, result.ExitCode)
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
		return []string{shellCommand, "-eu", shellCommandFlag, guestPackageInstallationScript(packageNames)}
	}
	arguments := []string{sudoCommand, backend.installCommand, installCommand, "-y"}
	return append(arguments, packageNames...)
}

func (backend guestPackageBackend) installPackages(project string, vmName string, packageNames []string, limactlCommand string, contexts ...context.Context) error {
	options := defaultProcessOptions(limactlCommand, contexts...)
	if _, err := runGuest(project, vmName, backend.installArguments(packageNames), options, nil); err != nil {
		return ljaError("cannot install dependencies %s with %s backend in VM %s: %w", strings.Join(packageNames, ", "), backend.name, vmName, err)
	}
	return nil
}

func guestPackageInstalled(project string, vmName string, packageName string, limactlCommand string) (bool, error) {
	backend, err := packageBackendForGuest(project, vmName, limactlCommand)
	if err != nil {
		return false, err
	}
	return backend.queryPackage(project, vmName, packageName, limactlCommand)
}

func ensureGuestPackages(project string, vmName string, packageNames []string, limactlCommand string) error {
	packageNames = uniqueStrings(packageNames)
	if len(packageNames) == 0 {
		return nil
	}
	backend, err := packageBackendForGuest(project, vmName, limactlCommand)
	if err != nil {
		return ljaError("cannot prepare dependencies %s in VM %s: %w", strings.Join(packageNames, ", "), vmName, err)
	}
	missing := make([]string, 0, len(packageNames))
	for _, packageName := range packageNames {
		installed, queryErr := backend.queryPackage(project, vmName, packageName, limactlCommand)
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

	slog.Info("installing packages", "backend", backend.name, "packages", missing, "vm", vmName)
	if err := backend.installPackages(project, vmName, missing, limactlCommand); err != nil {
		return err
	}
	for _, packageName := range missing {
		installed, queryErr := backend.queryPackage(project, vmName, packageName, limactlCommand)
		if queryErr != nil {
			return queryErr
		}
		if !installed {
			return ljaError("cannot verify dependency %s with %s backend in VM %s: installation did not provide the requested package", packageName, backend.name, vmName)
		}
	}
	return nil
}
