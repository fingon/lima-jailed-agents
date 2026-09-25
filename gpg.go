package lja

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	gpgUnixNetwork        = "unix"
	gpgSSHCommand         = "ssh"
	gpgTemporaryDirectory = "/tmp"
	gpgCommand            = "gpg"
	gpgConfCommand        = "gpgconf"
	gpgHomeEnv            = "GNUPGHOME"
	gpgNoAutostartFlag    = "--no-autostart"
	gpgPackage            = "gnupg"
	gpgSocketName         = "S.gpg-agent"
	gpgSessionPrefix      = "/tmp/lja-gpg-"
	gpgStartupTimeout     = 30 * time.Second
	gpgCleanupTimeout     = 5 * time.Second
	gpgProbeInterval      = 100 * time.Millisecond
)

func (config DevelopmentConfig) validateGPGEnvironment(environment map[string]string) error {
	if !config.GPGForwarding {
		return nil
	}
	for _, values := range []map[string]string{config.Env, environment} {
		if _, present := values[gpgHomeEnv]; present {
			return ljaError("%s is managed by LJA when gpg_forwarding is enabled", gpgHomeEnv)
		}
	}
	if slices.Contains(config.EnvPassthrough, gpgHomeEnv) {
		return ljaError("%s cannot be passed through when gpg_forwarding is enabled", gpgHomeEnv)
	}
	return nil
}

type gpgWorkflow struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	session *gpgSession
}

func (options *WorkflowOptions) ownGPGWorkflow() (func(*error), error) {
	config := developmentConfigOrDefault(options.Development)
	if !config.GPGForwarding || options.gpg != nil {
		return func(*error) {}, nil
	}
	if err := config.validateGPGEnvironment(options.Environment); err != nil {
		return nil, err
	}
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancelCause(signalContext)
	options.gpg = &gpgWorkflow{ctx: ctx, cancel: cancel}
	return func(result *error) {
		*result = errors.Join(*result, options.gpg.closeSession())
		if cause := context.Cause(ctx); cause != nil {
			*result = errors.Join(*result, cause)
		}
		cancel(nil)
		stop()
	}, nil
}

func (workflow *gpgWorkflow) closeSession() error {
	if workflow == nil || workflow.session == nil {
		return nil
	}
	session := workflow.session
	workflow.session = nil
	return session.close()
}

func (workflow *gpgWorkflow) environment(project, vm, lima string, environment map[string]string) (map[string]string, error) {
	if workflow == nil {
		return environment, nil
	}
	if err := workflow.ctx.Err(); err != nil {
		return nil, context.Cause(workflow.ctx)
	}
	if workflow.session == nil || workflow.session.vm != vm {
		if err := workflow.closeSession(); err != nil {
			return nil, err
		}
		session, err := startGPGSession(gpgSessionOptions{
			parent:  workflow.ctx,
			cancel:  workflow.cancel,
			project: project,
			vm:      vm,
			lima:    lima,
		})
		if err != nil {
			return nil, err
		}
		workflow.session = session
	}
	result := make(map[string]string, len(environment)+1)
	maps.Copy(result, environment)
	result[gpgHomeEnv] = workflow.session.guestHome
	return result, nil
}

func (workflow *gpgWorkflow) context() context.Context {
	if workflow == nil {
		return nil
	}
	return workflow.ctx
}

func (workflow *gpgWorkflow) processOptions(lima string) processOptions {
	options := defaultProcessOptions(lima)
	if workflow != nil {
		options.context = workflow.ctx
	}
	return options
}

type gpgProxy struct {
	listener    net.Listener
	target      string
	mutex       sync.Mutex
	connections map[net.Conn]bool
	closed      bool
	workers     sync.WaitGroup
	cancel      context.CancelCauseFunc
}

func newGPGProxy(path, target string, cancel context.CancelCauseFunc) (*gpgProxy, error) {
	listener, err := net.Listen(gpgUnixNetwork, path)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on GPG proxy: %w", err)
	}
	proxy := &gpgProxy{listener: listener, target: target, connections: make(map[net.Conn]bool), cancel: cancel}
	proxy.workers.Add(1)
	go proxy.accept()
	return proxy, nil
}

func (proxy *gpgProxy) fail(err error) {
	proxy.mutex.Lock()
	closed := proxy.closed
	proxy.mutex.Unlock()
	if !closed {
		proxy.cancel(fmt.Errorf("GPG forwarding failed: %w", err))
	}
}

func (proxy *gpgProxy) track(connection net.Conn) bool {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	if proxy.closed {
		return false
	}
	proxy.connections[connection] = true
	return true
}

func closeGPGConnection(connection net.Conn) {
	if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Warn("cannot close GPG connection", "error", err)
	}
}

func (proxy *gpgProxy) release(connection net.Conn) {
	proxy.mutex.Lock()
	delete(proxy.connections, connection)
	proxy.mutex.Unlock()
	closeGPGConnection(connection)
}

func (proxy *gpgProxy) accept() {
	defer proxy.workers.Done()
	for {
		connection, err := proxy.listener.Accept()
		if err != nil {
			proxy.fail(err)
			return
		}
		if !proxy.track(connection) {
			closeGPGConnection(connection)
			return
		}
		proxy.workers.Add(1)
		go proxy.relay(connection)
	}
}

func (proxy *gpgProxy) relay(connection net.Conn) {
	defer proxy.workers.Done()
	defer proxy.release(connection)
	target, err := net.DialTimeout(gpgUnixNetwork, proxy.target, gpgCleanupTimeout)
	if err != nil {
		proxy.fail(err)
		return
	}
	if !proxy.track(target) {
		closeGPGConnection(target)
		return
	}
	defer proxy.release(target)
	unixTarget, ok := target.(*net.UnixConn)
	if !ok {
		proxy.fail(ljaError("GPG proxy target is not a Unix socket"))
		return
	}
	var copies sync.WaitGroup
	copies.Go(func() {
		if _, err := io.Copy(target, connection); err != nil && !errors.Is(err, net.ErrClosed) {
			proxy.fail(err)
		}
		if err := unixTarget.CloseWrite(); err != nil && !errors.Is(err, net.ErrClosed) {
			proxy.fail(err)
		}
	})
	if _, err := io.Copy(connection, target); err != nil && !errors.Is(err, net.ErrClosed) {
		proxy.fail(err)
	}
	closeGPGConnection(connection)
	copies.Wait()
}

func (proxy *gpgProxy) close() error {
	proxy.mutex.Lock()
	proxy.closed = true
	err := proxy.listener.Close()
	for connection := range proxy.connections {
		closeGPGConnection(connection)
	}
	proxy.mutex.Unlock()
	proxy.workers.Wait()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type gpgSession struct {
	project        string
	vm             string
	lima           string
	guestHome      string
	guestSocket    string
	hostDirectory  string
	proxy          *gpgProxy
	ssh            *exec.Cmd
	sshDone        chan struct{}
	sshErr         error
	cancelSSH      context.CancelFunc
	stopRevocation func() bool
}

type gpgSessionOptions struct {
	parent  context.Context
	cancel  context.CancelCauseFunc
	project string
	vm      string
	lima    string
}

func gpgHostCommand(ctx context.Context, command string, arguments ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, arguments...)
	cmd.WaitDelay = processPipeWaitTimeout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("host %s failed (check GnuPG installation and host pinentry): %w: %s", command, err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func gpgAbsolutePath(output []byte) (string, error) {
	path := strings.TrimSuffix(string(output), "\n")
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", ljaError("invalid absolute GPG/SSH path returned by command")
	}
	return path, nil
}

func gpgSSHArguments(config, vm, guestSocket, hostSocket string) []string {
	arguments := []string{"-F", config, "-S", "none"}
	for _, option := range []string{
		"ControlMaster=no", "ControlPersist=no", "ForwardAgent=no", "ForwardX11=no",
		"BatchMode=yes", "ExitOnForwardFailure=yes", "StreamLocalBindUnlink=no",
		"ServerAliveInterval=5", "ServerAliveCountMax=2",
	} {
		arguments = append(arguments, "-o", option)
	}
	return append(arguments, "-T", "-N", "-R", guestSocket+":"+hostSocket, "lima-"+vm)
}

func startGPGSession(sessionOptions gpgSessionOptions) (result *gpgSession, returnErr error) {
	ctx, stop := context.WithTimeout(sessionOptions.parent, gpgStartupTimeout)
	defer stop()
	session := &gpgSession{project: sessionOptions.project, vm: sessionOptions.vm, lima: sessionOptions.lima}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, session.close())
		}
	}()
	if _, err := exec.LookPath(gpgSSHCommand); err != nil {
		return nil, fmt.Errorf("GPG forwarding requires OpenSSH: %w", err)
	}
	if _, err := gpgHostCommand(ctx, gpgConfCommand, "--launch", "gpg-agent"); err != nil {
		return nil, err
	}
	output, err := gpgHostCommand(ctx, gpgConfCommand, "--list-dirs", "agent-extra-socket")
	if err != nil {
		return nil, err
	}
	hostSocket, err := gpgAbsolutePath(output)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(hostSocket)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect host GPG extra socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, ljaError("host GPG extra socket is not a Unix socket")
	}
	publicKeys, err := gpgHostCommand(ctx, gpgCommand, "--batch", "--export")
	if err != nil {
		return nil, err
	}
	options := defaultProcessOptions(sessionOptions.lima)
	options.context = ctx
	options.captureOutput = true
	metadata, err := runLima([]string{"list", "--format={{.SSHConfigFile}}", sessionOptions.vm}, options)
	if err != nil {
		return nil, err
	}
	sshConfig, err := gpgAbsolutePath(metadata.Stdout)
	if err != nil {
		return nil, err
	}
	home, err := runGuest(sessionOptions.project, sessionOptions.vm, []string{"mktemp", "-d", gpgSessionPrefix + "XXXXXXXXXX"}, guestExecution(options, nil))
	if err != nil {
		return nil, err
	}
	guestHome, err := gpgAbsolutePath(home.Stdout)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(guestHome, gpgSessionPrefix) || strings.Contains(strings.TrimPrefix(guestHome, gpgSessionPrefix), "/") {
		return nil, ljaError("invalid guest GPG temporary directory")
	}
	session.guestHome = guestHome
	environment := map[string]string{gpgHomeEnv: session.guestHome}
	setup := `set -eu
umask 077
printf '%s\n' no-autostart > "$GNUPGHOME/gpg.conf"
gpg_socket=$(gpgconf --list-dirs agent-socket)
if [ "${gpg_socket%/*}" != "$GNUPGHOME" ]; then gpgconf --create-socketdir; fi
printf '%s\n' "$gpg_socket"
`
	socket, err := runGuest(sessionOptions.project, sessionOptions.vm, []string{shellCommand, shellCommandFlag, setup}, guestExecution(options, environment))
	if err != nil {
		return nil, err
	}
	session.guestSocket, err = gpgAbsolutePath(socket.Stdout)
	if err != nil {
		return nil, err
	}
	if filepath.Base(session.guestSocket) != gpgSocketName || strings.Contains(session.guestSocket, ":") {
		return nil, ljaError("unexpected guest GPG socket name")
	}
	if len(publicKeys) != 0 {
		options.hasInput = true
		options.inputData = publicKeys
		if _, err := runGuest(sessionOptions.project, sessionOptions.vm, []string{gpgCommand, "--batch", gpgNoAutostartFlag, "--import"}, guestExecution(options, environment)); err != nil {
			return nil, err
		}
		options.hasInput = false
		options.inputData = nil
	}
	session.hostDirectory, err = os.MkdirTemp(gpgTemporaryDirectory, "lja-gpg-")
	if err != nil {
		return nil, fmt.Errorf("cannot create GPG proxy directory: %w", err)
	}
	proxySocket := filepath.Join(session.hostDirectory, "agent")
	session.proxy, err = newGPGProxy(proxySocket, hostSocket, sessionOptions.cancel)
	if err != nil {
		return nil, err
	}
	session.stopRevocation = context.AfterFunc(sessionOptions.parent, func() {
		if err := session.proxy.close(); err != nil {
			slog.Error("cannot revoke GPG proxy", "vm", sessionOptions.vm, "error", err)
		}
	})
	sshContext, cancelSSH := context.WithCancel(sessionOptions.parent)
	session.cancelSSH = cancelSSH
	session.ssh = exec.CommandContext(sshContext, gpgSSHCommand, gpgSSHArguments(sshConfig, sessionOptions.vm, session.guestSocket, proxySocket)...)
	session.ssh.Stderr = os.Stderr
	if err := session.ssh.Start(); err != nil {
		return nil, fmt.Errorf("cannot start GPG SSH forwarding: %w", err)
	}
	session.sshDone = make(chan struct{})
	go func() {
		session.sshErr = session.ssh.Wait()
		if sshContext.Err() == nil {
			if session.sshErr != nil {
				sessionOptions.cancel(fmt.Errorf("GPG SSH tunnel exited unexpectedly: %w", session.sshErr))
			} else {
				sessionOptions.cancel(ljaError("GPG SSH tunnel exited unexpectedly"))
			}
		}
		close(session.sshDone)
	}()
	probeOptions := options
	probeOptions.check = false
	for {
		probe, err := runGuest(sessionOptions.project, sessionOptions.vm, []string{"test", "-S", session.guestSocket}, guestExecution(probeOptions, nil))
		if err != nil {
			return nil, err
		}
		if probe.ExitCode == 0 {
			break
		}
		if probe.ExitCode != 1 {
			return nil, guestConnectionError(sessionOptions.vm, probe)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("GPG forwarding startup failed: %w", context.Cause(ctx))
		case <-time.After(gpgProbeInterval):
		}
	}
	probe, err := runGuest(sessionOptions.project, sessionOptions.vm, []string{"gpg-connect-agent", gpgNoAutostartFlag, "GETINFO version", "/bye"}, guestExecution(options, environment))
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(probe.Stdout, []byte("\nOK")) && !bytes.HasPrefix(probe.Stdout, []byte("OK")) {
		return nil, ljaError("forwarded GPG agent did not acknowledge probe")
	}
	slog.Info("GPG forwarding ready", "vm", sessionOptions.vm)
	return session, nil
}

func (session *gpgSession) close() error {
	var result error
	if session.stopRevocation != nil {
		session.stopRevocation()
	}
	if session.proxy != nil {
		result = errors.Join(result, session.proxy.close())
	}
	if session.cancelSSH != nil {
		session.cancelSSH()
	}
	if session.sshDone != nil {
		<-session.sshDone
		if session.sshErr != nil {
			if exitError, ok := errors.AsType[*exec.ExitError](session.sshErr); !ok || exitError == nil {
				result = errors.Join(result, fmt.Errorf("cannot reap GPG SSH tunnel: %w", session.sshErr))
			}
		}
	}
	if session.hostDirectory != "" {
		result = errors.Join(result, os.RemoveAll(session.hostDirectory))
	}
	if session.guestHome != "" {
		ctx, cancel := context.WithTimeout(context.Background(), gpgCleanupTimeout)
		defer cancel()
		options := defaultProcessOptions(session.lima)
		options.context = ctx
		options.captureOutput = true
		script := "set -eu\n"
		if session.guestSocket != "" && filepath.Dir(session.guestSocket) != session.guestHome {
			script += "gpgconf --remove-socketdir\n"
		}
		script += "rm -rf -- \"$GNUPGHOME\"\n"
		if _, err := runGuest(session.project, session.vm, []string{shellCommand, shellCommandFlag, script}, guestExecution(options, map[string]string{gpgHomeEnv: session.guestHome})); err != nil {
			result = errors.Join(result, fmt.Errorf("cannot clean guest GPG session %s: %w", session.guestHome, err))
		}
	}
	return result
}
