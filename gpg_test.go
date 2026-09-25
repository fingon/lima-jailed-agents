package lja

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

const (
	gpgTestProcessEnv           = "LJA_TEST_GPG_PROCESS"
	gpgTestSocketEnv            = "LJA_TEST_GPG_SOCKET"
	gpgTestLogEnv               = "LJA_TEST_GPG_LOG"
	gpgTestFailureEnv           = "LJA_TEST_GPG_FAILURE"
	gpgTestCommand              = "gpg-test-command"
	gpgTestTimeout              = 5 * time.Second
	gpgTestReply                = "D 2.4.0\nOK\n"
	gpgForwardingEnabledConfig  = "gpg_forwarding: true"
	gpgForwardingDisabledConfig = "gpg_forwarding: false"
	gpgCustomHome               = "/custom"
	gpgTestFileCommand          = "test"
	gpgTestGitConfigFile        = "gitconfig"
	gpgPrepareOperation         = "prepare"
	gpgPrepareAgentsOperation   = "prepare-agents"
	gpgCleanupFailureName       = "cleanup"
	gpgExportFailureName        = "export"
	gpgImportFailureName        = "import"
	gpgLaunchFailureName        = "launch"
	gpgProbeFailureName         = "probe"
	gpgTunnelFailureName        = "tunnel"
)

func TestGPGConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, global, project string
		want                  bool
	}{
		{name: "default", global: "{}", project: "{}"},
		{name: "global", global: gpgForwardingEnabledConfig, project: "{}", want: true},
		{name: "project enable", global: gpgForwardingDisabledConfig, project: gpgForwardingEnabledConfig, want: true},
		{name: "project disable", global: gpgForwardingEnabledConfig, project: gpgForwardingDisabledConfig},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultDevelopmentConfig()
			for _, content := range []string{test.global, test.project} {
				source, err := decodeDevelopmentConfig([]byte(content), true)
				assert.NilError(t, err)
				config = applyDevelopmentConfig(config, source, "test")
			}
			assert.Equal(t, config.GPGForwarding, test.want)
			assert.Equal(t, config.clone().GPGForwarding, test.want)
			assert.Equal(t, config.AsYAML().GPGForwarding, test.want)
			assert.Equal(t, slices.Contains(effectiveDevelopmentPackages(config), gpgPackage), test.want)
		})
	}
	for _, content := range []string{"gpg_forwarding: null", "gpg_forwarding: 'true'", "gpg_forwarding: 1", "gpg_forwarding: []", "gpg_forwarding: true\ngpg_forwarding: false"} {
		assert.Assert(t, ValidateDevelopmentConfig([]byte(content), true) != nil)
	}
	for _, test := range []struct {
		name        string
		enabled     bool
		env         map[string]string
		passthrough []string
		fail        bool
	}{
		{name: "disabled custom home", env: map[string]string{gpgHomeEnv: gpgCustomHome}},
		{name: "enabled custom home", enabled: true, env: map[string]string{gpgHomeEnv: gpgCustomHome}, fail: true},
		{name: "enabled passthrough", enabled: true, passthrough: []string{gpgHomeEnv}, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DevelopmentConfig{GPGForwarding: test.enabled, Env: test.env, EnvPassthrough: test.passthrough}
			_, err := config.ResolveEnvironment(nil)
			assert.Equal(t, err != nil, test.fail)
		})
	}
}

func TestGPGGitPrograms(t *testing.T) {
	home := t.TempDir()
	for _, file := range []string{gpgTestGitConfigFile, "included"} {
		content, err := os.ReadFile(filepath.Join("testdata/gpg", file))
		assert.NilError(t, err)
		destination := file
		if file == gpgTestGitConfigFile {
			destination = gitConfigName
		}
		assert.NilError(t, os.WriteFile(filepath.Join(home, destination), content, 0o600))
	}
	for _, enabled := range []bool{false, true} {
		copies, err := gitConfigCopiesForGPG(home, enabled)
		assert.NilError(t, err)
		assert.Equal(t, bytes.Contains(copies[gitConfigName], []byte("/host/bin/gpg")), !enabled)
		assert.Equal(t, bytes.Contains(copies["included"], []byte("/host/other/gpg")), !enabled)
		assert.Assert(t, bytes.Contains(copies["included"], []byte("/host/bin/ssh-keygen")))
		assert.Assert(t, bytes.Contains(copies[gitConfigName], []byte("TESTKEY")))
	}
	original, err := os.ReadFile(filepath.Join(home, gitConfigName))
	assert.NilError(t, err)
	assert.Assert(t, bytes.Contains(original, []byte("/host/bin/gpg")))
}

func gpgSocketDirectory(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func gpgTestAgent(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "host")
	listener, err := net.Listen("unix", path)
	assert.NilError(t, err)
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer closeGPGConnection(connection)
				scanner := bufio.NewScanner(connection)
				for scanner.Scan() {
					if _, err := io.WriteString(connection, gpgTestReply); err != nil {
						return
					}
				}
			})
		}
	})
	t.Cleanup(func() {
		assert.NilError(t, listener.Close())
		workers.Wait()
	})
	return path
}

func TestGPGProxyRevokesActiveConnections(t *testing.T) {
	directory := gpgSocketDirectory(t)
	target := gpgTestAgent(t, directory)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	proxy, err := newGPGProxy(filepath.Join(directory, "proxy"), target, cancel)
	assert.NilError(t, err)
	connection, err := net.Dial("unix", proxy.listener.Addr().String())
	assert.NilError(t, err)
	defer closeGPGConnection(connection)
	assert.NilError(t, connection.SetDeadline(time.Now().Add(gpgTestTimeout)))
	_, err = io.WriteString(connection, "GETINFO version\n")
	assert.NilError(t, err)
	reply := make([]byte, len(gpgTestReply))
	_, err = io.ReadFull(connection, reply)
	assert.NilError(t, err)
	assert.Equal(t, string(reply), gpgTestReply)
	assert.NilError(t, proxy.close())
	_, err = connection.Read(reply)
	assert.Assert(t, err != nil)
	_, err = net.Dial("unix", proxy.listener.Addr().String())
	assert.Assert(t, err != nil)
	assert.NilError(t, ctx.Err())
}

func TestGPGProxyMissingAgentCancels(t *testing.T) {
	directory := gpgSocketDirectory(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	proxy, err := newGPGProxy(filepath.Join(directory, "proxy"), filepath.Join(directory, "absent"), cancel)
	assert.NilError(t, err)
	defer func() { assert.NilError(t, proxy.close()) }()
	connection, err := net.Dial("unix", proxy.listener.Addr().String())
	assert.NilError(t, err)
	defer closeGPGConnection(connection)
	select {
	case <-ctx.Done():
	case <-time.After(gpgTestTimeout):
		t.Fatal("proxy did not cancel")
	}
	assert.ErrorContains(t, context.Cause(ctx), "GPG forwarding failed")
}

func gpgFixture(t *testing.T, status string) (string, string, WorkflowOptions) {
	t.Helper()
	project, vm, options := vmFixture(t, status)
	directory := gpgSocketDirectory(t)
	t.Setenv(gpgTestSocketEnv, gpgTestAgent(t, directory))
	t.Setenv(gpgTestLogEnv, filepath.Join(directory, "log"))
	t.Setenv(gpgTestFailureEnv, "")
	script, err := os.ReadFile("testdata/gpg/command.sh")
	assert.NilError(t, err)
	bin := filepath.Join(t.TempDir(), "bin")
	assert.NilError(t, os.Mkdir(bin, 0o700))
	for _, name := range []string{gpgCommand, gpgConfCommand, gpgSSHCommand, limaCtlCommand} {
		assert.NilError(t, os.WriteFile(filepath.Join(bin, name), script, 0o700))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options.LimaCommand = filepath.Join(bin, limaCtlCommand)
	options.Development.GPGForwarding = true
	return project, vm, options
}

func TestGPGWorkflowLifetime(t *testing.T) {
	for _, operation := range []string{limaShellOperation, agentTestName, gpgPrepareOperation, gpgPrepareAgentsOperation, createCommandName, updateCommandName, recreateCommandName} {
		t.Run(operation, func(t *testing.T) {
			status := limaStatusRunning
			if operation == createCommandName {
				status = ""
			}
			project, _, options := gpgFixture(t, status)
			var err error
			switch operation {
			case limaShellOperation, recreateCommandName:
				options.Recreate = operation == recreateCommandName
				var code int
				code, err = OpenShell(project, []string{gpgTestCommand}, "", options)
				assert.Equal(t, code, 0)
			case agentTestName:
				_, err = RunAgent(project, AgentRunOptions{
					AgentName: codexAgentName,
					Arguments: []string{codeXLoginSubcommand},
					Workflow:  options,
				})
			case gpgPrepareOperation:
				_, err = PrepareVM(project, options)
			case gpgPrepareAgentsOperation:
				_, err = PrepareAgents(AgentPreparationOptions{
					Project:          project,
					SelectedAgent:    codexAgentName,
					TrustDirectories: []string{},
					Workflow:         options,
				})
			case createCommandName:
				_, err = CreateVM(project, options)
			case updateCommandName:
				_, err = InstallAgent(project, codexAgentName, true, options)
			}
			assert.NilError(t, err)
			log, readErr := os.ReadFile(os.Getenv(gpgTestLogEnv))
			assert.NilError(t, readErr)
			assert.Assert(t, bytes.Contains(log, []byte("--export")))
			assert.Assert(t, !bytes.Contains(log, []byte("--export-secret")))
			homes := gpgLoggedHomes(t, log)
			assert.Assert(t, len(homes) > 0)
			if operation == recreateCommandName {
				assert.Equal(t, len(homes), 2)
			} else {
				assert.Equal(t, len(homes), 1)
			}
			for _, home := range homes {
				_, statErr := os.Stat(home)
				assert.Assert(t, os.IsNotExist(statErr), "temporary home remains: %s", home)
			}
		})
	}
}

func gpgLoggedHomes(t *testing.T, log []byte) []string {
	t.Helper()
	homes := []string{}
	for line := range bytes.SplitSeq(bytes.TrimSpace(log), []byte("\n")) {
		var arguments []string
		assert.NilError(t, json.Unmarshal(line, &arguments))
		for _, argument := range arguments {
			if after, ok := strings.CutPrefix(argument, gpgHomeEnv+"="); ok {
				home := after
				if !slices.Contains(homes, home) {
					homes = append(homes, home)
				}
			}
		}
	}
	return homes
}

func TestGPGDisabledAndNoop(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			project, _, options := gpgFixture(t, limaStatusRunning)
			options.Development.GPGForwarding = enabled
			_, err := CreateVM(project, options)
			assert.NilError(t, err)
			if !enabled {
				_, err = OpenShell(project, []string{guestConnectionProbe}, "", options)
				assert.NilError(t, err)
			}
			log, err := os.ReadFile(os.Getenv(gpgTestLogEnv))
			assert.NilError(t, err)
			assert.Assert(t, !bytes.Contains(log, []byte("--export")))
			assert.Assert(t, !bytes.Contains(log, []byte("agent-extra-socket")))
		})
	}
}

func TestGPGSessionFailures(t *testing.T) {
	for _, failure := range []string{gpgLaunchFailureName, gpgExportFailureName, gpgSSHCommand, gpgImportFailureName, gpgProbeFailureName, gpgCleanupFailureName, guestCommandProbe, gpgTunnelFailureName} {
		t.Run(failure, func(t *testing.T) {
			project, _, options := gpgFixture(t, limaStatusRunning)
			t.Setenv(gpgTestFailureEnv, failure)
			code, err := OpenShell(project, []string{gpgTestCommand}, "", options)
			if failure == guestCommandProbe {
				assert.NilError(t, err)
				assert.Equal(t, code, 17)
			} else {
				assert.Assert(t, err != nil)
			}
			log, readErr := os.ReadFile(os.Getenv(gpgTestLogEnv))
			assert.NilError(t, readErr)
			for _, home := range gpgLoggedHomes(t, log) {
				connection, dialErr := net.DialTimeout("unix", filepath.Join(home, gpgSocketName), time.Second)
				if dialErr == nil {
					closeGPGConnection(connection)
					t.Fatal("forwarding survived failure")
				}
				assert.NilError(t, os.RemoveAll(home))
			}
		})
	}
}

func TestGPGConcurrentSessions(t *testing.T) {
	project, vm, options := gpgFixture(t, limaStatusRunning)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	first, err := startGPGSession(gpgSessionOptions{parent: ctx, cancel: cancel, project: project, vm: vm, lima: options.LimaCommand})
	assert.NilError(t, err)
	defer func() { assert.NilError(t, first.close()) }()
	second, err := startGPGSession(gpgSessionOptions{parent: ctx, cancel: cancel, project: project, vm: vm, lima: options.LimaCommand})
	assert.NilError(t, err)
	defer func() { assert.NilError(t, second.close()) }()
	assert.Assert(t, first.guestHome != second.guestHome)
	assert.Assert(t, first.guestSocket != second.guestSocket)
	assert.NilError(t, first.close())
	assert.NilError(t, gpgProbeSocket(second.guestSocket))
}

func TestGPGProcess(_ *testing.T) {
	if os.Getenv(gpgTestProcessEnv) != "1" {
		return
	}
	if err := runGPGProcess(); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(24)
		}
		os.Exit(23)
	}
	os.Exit(0)
}

func runGPGProcess() error {
	separator := slices.Index(os.Args, "--")
	arguments := os.Args[separator+1:]
	tool := arguments[0]
	arguments = arguments[1:]
	log, err := os.OpenFile(os.Getenv(gpgTestLogEnv), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(log).Encode(append([]string{tool}, arguments...))
	if err := errors.Join(encodeErr, log.Close()); err != nil {
		return err
	}
	failure := os.Getenv(gpgTestFailureEnv)
	switch tool {
	case gpgConfCommand:
		if arguments[0] == "--launch" {
			if failure == gpgLaunchFailureName {
				return errors.New("launch failure")
			}
			return nil
		}
		_, err := fmt.Fprintln(os.Stdout, os.Getenv(gpgTestSocketEnv))
		return err
	case gpgCommand:
		if failure == gpgExportFailureName {
			return errors.New("export failure")
		}
		public, err := os.ReadFile("testdata/gpg/public.asc")
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(public)
		return err
	case gpgSSHCommand:
		if failure == gpgSSHCommand {
			return errors.New("ssh failure")
		}
		index := slices.Index(arguments, "-R")
		guestSocket, hostSocket, ok := strings.Cut(arguments[index+1], ":")
		if !ok {
			return errors.New("missing socket forwarding")
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		proxy, err := newGPGProxy(guestSocket, hostSocket, cancel)
		if err != nil {
			return err
		}
		if path := os.Getenv("LJA_TEST_SSH_PID"); path != "" {
			if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				return errors.Join(err, proxy.close())
			}
		}
		<-ctx.Done()
		return errors.Join(context.Cause(ctx), proxy.close())
	case limaCtlCommand:
		return runGPGTestLima(arguments, separator, failure)
	}
	return fmt.Errorf("unexpected test tool: %s", tool)
}

func runGPGTestLima(arguments []string, separator int, failure string) error {
	if len(arguments) > 1 && arguments[1] == "--format={{.SSHConfigFile}}" {
		_, err := fmt.Fprintln(os.Stdout, "/tmp/fake ssh.config")
		return err
	}
	if len(arguments) == 0 || arguments[0] != limaShellOperation {
		return runGPGVMProcess(arguments, separator)
	}
	return runGPGTestShell(arguments, separator, failure)
}

func runGPGTestShell(arguments []string, separator int, failure string) error {
	index := 4
	if arguments[1] == limaNoninteractiveFlag {
		index++
	}
	environment := map[string]string{}
	for index < len(arguments) {
		key, value, ok := strings.Cut(arguments[index], "=")
		if !ok {
			break
		}
		environment[key] = value
		index++
	}
	guest := unwrapGuestPathArguments(arguments[index:])
	if len(guest) == 0 {
		return runGPGVMProcess(arguments, separator)
	}
	handled, err := runGPGTestGuestCommand(guest, environment[gpgHomeEnv], failure)
	if handled {
		return err
	}
	return runGPGVMProcess(arguments, separator)
}

func runGPGVMProcess(arguments []string, separator int) error {
	os.Args = append(os.Args[:separator+1], arguments...)
	return runVMProcess()
}

func runGPGTestGuestCommand(guest []string, home, failure string) (bool, error) {
	switch guest[0] {
	case "mktemp":
		directory, err := os.MkdirTemp("/tmp", "lja-gpg-")
		if err != nil {
			return true, err
		}
		_, err = fmt.Fprintln(os.Stdout, directory)
		return true, err
	case gpgCommand:
		return true, runGPGTestImport(failure)
	case gpgTestFileCommand:
		info, err := os.Stat(guest[2])
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			os.Exit(1)
		}
		return true, nil
	case "gpg-connect-agent":
		if failure == gpgProbeFailureName {
			_, err := fmt.Fprintln(os.Stdout, "ERR rejected")
			return true, err
		}
		if err := gpgProbeSocket(filepath.Join(home, gpgSocketName)); err != nil {
			return true, err
		}
		_, err := fmt.Fprint(os.Stdout, gpgTestReply)
		return true, err
	case gpgTestCommand, codexAgentName:
		return true, runGPGTestCommand(home, failure)
	case shellCommand:
		return runGPGTestShellCommand(guest, home, failure)
	default:
		return false, nil
	}
}

func runGPGTestImport(failure string) error {
	if failure == gpgImportFailureName {
		return errors.New("import failure")
	}
	public, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	expected, err := os.ReadFile("testdata/gpg/public.asc")
	if err != nil {
		return err
	}
	if !bytes.Equal(public, expected) {
		return errors.New("incorrect public import")
	}
	return nil
}

func runGPGTestCommand(home, failure string) error {
	if err := gpgProbeSocket(filepath.Join(home, gpgSocketName)); err != nil {
		return err
	}
	if failure == guestCommandProbe {
		os.Exit(17)
	}
	if failure == gpgTunnelFailureName {
		if err := os.Remove(os.Getenv(gpgTestSocketEnv)); err != nil {
			return err
		}
		return gpgProbeSocket(filepath.Join(home, gpgSocketName))
	}
	return nil
}

func runGPGTestShellCommand(guest []string, home, failure string) (bool, error) {
	script := guest[len(guest)-1]
	if strings.Contains(script, "--create-socketdir") {
		_, err := fmt.Fprintln(os.Stdout, filepath.Join(home, gpgSocketName))
		return true, err
	}
	if strings.Contains(script, "rm -rf --") {
		if failure == gpgCleanupFailureName {
			return true, errors.New("cleanup failure")
		}
		return true, os.RemoveAll(home)
	}
	if script == testSetupCommand && home != "" {
		if err := gpgProbeSocket(filepath.Join(home, gpgSocketName)); err != nil {
			return true, err
		}
	}
	return false, nil
}

func gpgProbeSocket(path string) error {
	connection, err := net.DialTimeout("unix", path, gpgTestTimeout)
	if err != nil {
		return err
	}
	defer closeGPGConnection(connection)
	if err := connection.SetDeadline(time.Now().Add(gpgTestTimeout)); err != nil {
		return err
	}
	if _, err := io.WriteString(connection, "GETINFO version\n"); err != nil {
		return err
	}
	reply := make([]byte, len(gpgTestReply))
	if _, err := io.ReadFull(connection, reply); err != nil {
		return err
	}
	if string(reply) != gpgTestReply {
		return errors.New("invalid agent reply")
	}
	return nil
}

func TestGPGSSHArguments(t *testing.T) {
	arguments := gpgSSHArguments("/space and 'quote/config", "vm", "/tmp/guest", "/tmp/host")
	assert.Equal(t, arguments[1], "/space and 'quote/config")
	for _, option := range []string{"ControlMaster=no", "ControlPersist=no", "ForwardAgent=no", "ExitOnForwardFailure=yes"} {
		assert.Assert(t, slices.Contains(arguments, option))
	}
	assert.Assert(t, slices.Contains(arguments, "/tmp/guest:/tmp/host"))
}

func TestGPGWorkflowProcess(_ *testing.T) {
	if os.Getenv("LJA_TEST_GPG_OWNER") != "1" {
		return
	}
	options := WorkflowOptions{Development: &DevelopmentConfig{GPGForwarding: true}}
	cleanup, err := options.ownGPGWorkflow()
	if err != nil {
		os.Exit(23)
	}
	_, err = options.gpg.environment(os.Getenv("LJA_TEST_GPG_PROJECT"), "test-vm", os.Getenv("LJA_TEST_GPG_LIMA"), nil)
	if err == nil {
		err = os.WriteFile(os.Getenv("LJA_TEST_GPG_READY"), []byte(options.gpg.session.guestSocket), 0o600)
	}
	if err == nil {
		<-options.gpg.ctx.Done()
	}
	cleanup(&err)
	if err != nil {
		os.Exit(23)
	}
	os.Exit(0)
}

func TestGPGParentTerminationRevokesAccess(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			project, _, options := gpgFixture(t, limaStatusRunning)
			directory := t.TempDir()
			ready := filepath.Join(directory, "ready")
			sshPID := filepath.Join(directory, "ssh-pid")
			t.Setenv("LJA_TEST_GPG_OWNER", "1")
			t.Setenv("LJA_TEST_GPG_PROJECT", project)
			t.Setenv("LJA_TEST_GPG_LIMA", options.LimaCommand)
			t.Setenv("LJA_TEST_GPG_READY", ready)
			t.Setenv("LJA_TEST_SSH_PID", sshPID)
			binary, err := os.Executable()
			assert.NilError(t, err)
			cmd := exec.Command(binary, "-test.run=^TestGPGWorkflowProcess$")
			cmd.Stderr = os.Stderr
			assert.NilError(t, cmd.Start())
			t.Cleanup(func() {
				if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
					t.Errorf("cannot terminate GPG workflow process: %v", killErr)
				}
			})
			deadline := time.Now().Add(gpgTestTimeout)
			var socket []byte
			for time.Now().Before(deadline) {
				socket, err = os.ReadFile(ready)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			assert.NilError(t, err)
			connection, err := net.Dial("unix", string(socket))
			assert.NilError(t, err)
			defer closeGPGConnection(connection)
			assert.NilError(t, connection.SetDeadline(time.Now().Add(gpgTestTimeout)))
			_, err = io.WriteString(connection, "GETINFO version\n")
			assert.NilError(t, err)
			reply := make([]byte, len(gpgTestReply))
			_, err = io.ReadFull(connection, reply)
			assert.NilError(t, err)
			assert.NilError(t, cmd.Process.Signal(signal))
			assert.Assert(t, cmd.Wait() != nil)
			_, err = connection.Read(reply)
			assert.Assert(t, err != nil)
			if signal == syscall.SIGKILL {
				pidBytes, err := os.ReadFile(sshPID)
				assert.NilError(t, err)
				pid, err := strconv.Atoi(string(pidBytes))
				assert.NilError(t, err)
				process, err := os.FindProcess(pid)
				assert.NilError(t, err)
				if killErr := process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
					t.Errorf("cannot terminate GPG SSH process: %v", killErr)
				}
				assert.NilError(t, os.RemoveAll(filepath.Dir(string(socket))))
				log, err := os.ReadFile(os.Getenv(gpgTestLogEnv))
				assert.NilError(t, err)
				for line := range bytes.SplitSeq(bytes.TrimSpace(log), []byte("\n")) {
					var arguments []string
					assert.NilError(t, json.Unmarshal(line, &arguments))
					if len(arguments) > 0 && arguments[0] == gpgSSHCommand {
						index := slices.Index(arguments, "-R")
						_, hostSocket, ok := strings.Cut(arguments[index+1], ":")
						assert.Assert(t, ok)
						assert.NilError(t, os.RemoveAll(filepath.Dir(hostSocket)))
					}
				}
			}
		})
	}
}

func TestGPGSetupFailureRevokesBeforeRecreationCleanup(t *testing.T) {
	project, _, options := gpgFixture(t, limaStatusRunning)
	options.Recreate = true
	t.Setenv(vmFailureEnv, configSetup)
	_, err := OpenShell(project, []string{gpgTestCommand}, "", options)
	assert.ErrorContains(t, err, configSetup)
	log, err := os.ReadFile(os.Getenv(gpgTestLogEnv))
	assert.NilError(t, err)
	for _, home := range gpgLoggedHomes(t, log) {
		_, err := os.Stat(home)
		assert.Assert(t, os.IsNotExist(err))
	}
	assert.Assert(t, !bytes.Contains(log, []byte(gpgTestCommand)))
	cleanupIndex := bytes.Index(log, []byte("rm -rf --"))
	deleteIndex := bytes.Index(log, []byte(fmt.Sprintf(`%q`, deleteCommandName)))
	assert.Assert(t, cleanupIndex >= 0 && deleteIndex > cleanupIndex)
}

func TestGPGRejectsConfiguredHomeBeforePreparation(t *testing.T) {
	project, _, options := gpgFixture(t, limaStatusRunning)
	options.Environment = map[string]string{gpgHomeEnv: gpgCustomHome}
	_, err := PrepareVM(project, options)
	assert.ErrorContains(t, err, "managed by LJA")
	_, err = os.Stat(os.Getenv(gpgTestLogEnv))
	assert.Assert(t, os.IsNotExist(err))
	assert.ErrorContains(t, ValidateDevelopmentConfig([]byte("gpg_forwarding: true\nenv:\n  GNUPGHOME: /custom\n"), true), "managed by LJA")
}

func TestGPGPathValidation(t *testing.T) {
	for _, test := range []struct {
		value string
		valid bool
	}{
		{value: "/tmp/path with spaces\n", valid: true},
		{value: "/tmp/'quote'", valid: true},
		{value: "relative"},
		{value: ""},
		{value: "/tmp/one\n/tmp/two\n"},
		{value: "/tmp/zero\x00"},
	} {
		_, err := gpgAbsolutePath([]byte(test.value))
		assert.Equal(t, err == nil, test.valid)
	}
}
