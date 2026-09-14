package lja

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestGPGRealAgent(t *testing.T) {
	if os.Getenv("LJA_TEST_REAL_GPG") != "1" {
		t.Skip("set LJA_TEST_REAL_GPG=1 to exercise a disposable local GnuPG agent")
	}
	directory := gpgSocketDirectory(t)
	host := filepath.Join(directory, "host-home")
	guest := filepath.Join(directory, "guest-home")
	for _, home := range []string{host, guest} {
		assert.NilError(t, os.Mkdir(home, 0o700))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run := func(home, tool string, input []byte, arguments ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, tool, append([]string{"--homedir", home}, arguments...)...)
		cmd.Stdin = bytes.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			t.Logf("%s: %s", tool, stderr.Bytes())
		}
		return output, err
	}
	t.Cleanup(func() {
		cleanup := exec.Command(gpgConfCommand, "--homedir", host, "--kill", "all")
		output, err := cleanup.CombinedOutput()
		assert.NilError(t, err, "%s", output)
	})
	identity := "LJA Disposable Test <lja@example.invalid>"
	_, err := run(host, gpgCommand, nil, "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", identity, "rsa2048", "sign,encr", "0")
	assert.NilError(t, err)
	public, err := run(host, gpgCommand, nil, "--batch", "--export")
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(guest, "gpg.conf"), []byte("no-autostart\n"), 0o600))
	socket, err := run(guest, gpgConfCommand, nil, "--list-dirs", "agent-socket")
	assert.NilError(t, err)
	guestSocket, err := gpgAbsolutePath(socket)
	assert.NilError(t, err)
	if filepath.Dir(guestSocket) != guest {
		_, err = run(guest, gpgConfCommand, nil, "--create-socketdir")
		assert.NilError(t, err)
	}
	_, err = run(guest, gpgCommand, public, "--batch", "--no-autostart", "--import")
	assert.NilError(t, err)
	socket, err = run(host, gpgConfCommand, nil, "--list-dirs", "agent-extra-socket")
	assert.NilError(t, err)
	hostSocket, err := gpgAbsolutePath(socket)
	assert.NilError(t, err)
	proxyContext, cancelProxy := context.WithCancelCause(ctx)
	defer cancelProxy(nil)
	proxy, err := newGPGProxy(guestSocket, hostSocket, cancelProxy)
	assert.NilError(t, err)
	defer proxy.close()
	message := []byte("LJA GPG forwarding integration test\n")
	signature, err := run(guest, gpgCommand, message, "--batch", "--local-user", identity, "--detach-sign")
	assert.NilError(t, err)
	messagePath := filepath.Join(directory, "message")
	signaturePath := filepath.Join(directory, "signature")
	assert.NilError(t, os.WriteFile(messagePath, message, 0o600))
	assert.NilError(t, os.WriteFile(signaturePath, signature, 0o600))
	_, err = run(host, gpgCommand, nil, "--batch", "--verify", signaturePath, messagePath)
	assert.NilError(t, err)
	ciphertext, err := run(host, gpgCommand, message, "--batch", "--recipient", identity, "--encrypt")
	assert.NilError(t, err)
	plaintext, err := run(guest, gpgCommand, ciphertext, "--batch", "--decrypt")
	assert.NilError(t, err)
	assert.DeepEqual(t, plaintext, message)
	privateFiles, err := filepath.Glob(filepath.Join(guest, "private-keys-v1.d", "*"))
	assert.NilError(t, err)
	assert.Equal(t, len(privateFiles), 0)
	assert.NilError(t, proxyContext.Err())
	assert.NilError(t, proxy.close())
	_, err = run(guest, gpgCommand, message, "--batch", "--local-user", identity, "--detach-sign")
	assert.Assert(t, err != nil)
	if filepath.Dir(guestSocket) != guest {
		_, err = run(guest, gpgConfCommand, nil, "--remove-socketdir")
		assert.NilError(t, err)
	}
}
