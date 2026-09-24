# LJA — Lima Jailed Agents

LJA runs Codex, Claude Code, and OpenCode in a persistent Lima VM dedicated to
one project. The project is mounted writable, so changes made by an agent are
immediately visible on the host. Agent state is kept in an LJA-owned directory
so credentials, sessions, and settings do not get mixed with another project's
VM unless that is explicitly selected.

The Go CLI is in `cmd/lja`; the reusable workflow is in the root `lja` package.
Lima is the only host-side runtime dependency.

## Install and check

Install the latest LJA command from GitHub (preferred) with:

```sh
go install github.com/fingon/lima-jailed-agents/cmd/lja@latest
```

Go installs the command into the Go bin directory, which must be on `PATH`.

Go, pipx, and prek are installed through the platform package manager and pipx
with:

```sh
make dep
```

On macOS this uses Homebrew for Go and pipx. On Ubuntu it uses the system
packages (`golang-go` and `pipx`) and installs prek with pipx. The target leaves
existing Go, pipx, and prek installations unchanged, and installs the
repository hook.

Run all checks with:

```sh
make check
```

To run the CLI from a checkout:

```sh
go run ./cmd/lja --help
```

To install the command from a checkout:

```sh
make install
```

LJA requires an installed `limactl`. Agent packages and development packages
are installed inside the guest, not on the host.

Guest commands preserve the guest `PATH` and add user-installed executable
directories, including `$HOME/.local/bin` for pipx and Go's effective
`GOBIN` or `GOPATH/bin`. This applies to `lja shell`,
`lja make`, setup commands, and agent launches; host PATH settings are
not passed into the VM.

## Commands

```text
lja [OPTIONS] codex [-- AGENT_ARGS...]
lja [OPTIONS] claude [-- AGENT_ARGS...]
lja [OPTIONS] opencode [-- AGENT_ARGS...]
lja [OPTIONS] shell [-- SHELL_ARGS...]
lja [OPTIONS] make [-- MAKE_ARGS...]
lja [OPTIONS] create [--recreate]
lja [OPTIONS] config
lja [OPTIONS] status
lja [OPTIONS] stop [-a|--all]
lja [OPTIONS] delete [-a|--all]
lja [OPTIONS] update AGENT
```

Useful examples:

```sh
lja --project ~/src/example codex -- exec "Review the current change"
lja --project ~/src/example claude -- auth login
lja --project ~/src/example --with-agent claude --with-agent opencode codex
lja --project ~/src/example shell -- make test
lja --project ~/src/example make -- test
lja --project ~/src/example create
lja --project ~/src/example create --recreate
lja --project ~/src/example --recreate shell
lja --project ~/src/example status
lja --project ~/src/example stop
lja stop --all
lja --project ~/src/example delete
lja delete -a
```

Options before the command select the project and storage:

- `--project PATH` selects that exact project directory.
- `--state-dir PATH` selects a shared state root.
- `--project-state` stores state below the project instead.
- `--with-agent AGENT` prepares another agent in the same VM. It is repeatable.
- `--recreate` replaces the VM before create, shell, make, agent launch, or update.
- `-v` enables debug logging.

Arguments after the optional `--` separator are forwarded as raw argument
vectors. LJA does not perform host shell expansion on them. The `make` command
invokes the guest `make` executable directly in the current working directory
and forwards its arguments unchanged. The selected agent receives its normal
guest executable name and gets an execution permission default; native login
commands are forwarded without that default. OpenCode is started with an
allow-permission configuration. Explicit native permission options always take
precedence.

## Project discovery and VMs

When `--project` is omitted, LJA starts at the canonical current directory and
searches upward for the nearest `.lja.yaml` or deterministic LJA VM. If no
marker is found, the current directory is used. Discovery stops before your home
directory or any directory owned by another user. Container bases must be owned
by you and must not be your home directory, including with `--project`. There is no automatic Git-root
discovery. The current invocation directory remains the guest working directory
even when an ancestor is selected as the project root.

Each project gets a deterministic VM name:

```text
lja-<lowercase-ascii-basename>-<first-12-sha256-hex-digits>
```

The hash is computed from the canonical absolute project path, so symlink
aliases share a VM while equal names in different directories do not.

LJA creates or starts only a VM whose effective writable mounts exactly match
the selected storage. Project-local mode mounts the project. Shared mode mounts
the project and the selected state root at their same absolute guest paths.
Mismatched existing VMs are rejected unless `--recreate` is explicit; LJA
never silently deletes, recreates, or migrates them. Use `--project-state` to continue using an older project-local
VM, or use `lja create --recreate` when changing storage.

The default shared state root is
`${XDG_DATA_HOME:-~/.local/share}/lja/agents`. `LJA_STATE_DIR` is an environment
override, and `--state-dir` has higher precedence. Shared state may not overlap
the project. The selected state root is used consistently for all agents:

| Agent | State mapping |
| --- | --- |
| Codex | `<state-root>/.codex` through `CODEX_HOME` |
| Claude Code | `<state-root>/.claude` through `CLAUDE_CONFIG_DIR` |
| OpenCode | `<state-root>/.opencode` plus isolated XDG config/data/state/cache paths |

`status` only inspects and validates the VM. `stop` is a successful no-op for
an absent or already stopped project VM. `stop --all` and `delete --all` affect
only VMs in the `lja-` namespace and do not need a project or state selection.

`delete` runs `limactl delete -f` and succeeds if the project VM is already
absent. It works even when existing mounts differ from the current storage
configuration. Deletion removes the VM and its guest disk; host project files
and agent state are preserved. Both bulk commands also accept `-a`.

`create` prepares an absent VM (boot, packages, Git configuration, setup, and
configured default agents) without launching an agent. An existing VM is left
unchanged after mount validation when no default agents are configured. If
defaults are configured, a stopped existing VM is started and those agents
are installed or reused.

`create --recreate` prepares a new VM before stopping and replacing the old
one. It deletes the old guest disk only after the replacement starts under the
project's VM name. The same flag works before shell, agent, and update commands.
Failed preparation leaves the old VM intact; failed startup after promotion
attempts rollback. Lima rename is not atomic, so a failed rename preserves both
directories and reports recovery details. Setup changes to shared host files
cannot be rolled back. Interrupted replacements may leave `lja-new-*` or
`lja-old-*` VMs; inspect these before cleanup.

## Development configuration

LJA optionally reads these YAML mappings, in order:

```text
${XDG_CONFIG_HOME:-~/.config}/lja/config.yaml
<selected-project>/.lja.yaml
```

The project file is not searched for in ancestors after project selection.
Legacy JSON filenames are ignored. `config` prints the merged result as
deterministic YAML without resolving caller environment values.

```yaml
lima:
  cpus: 4
  memory: "8GiB"
packages: [git, make, ninja-build]
agents: [codex, claude]
copy_git_config: true
env:
  BUILD_MODE: development
  BUILD_COUNT: "3"
env_passthrough: [HTTPS_PROXY]
setup:
  - |
    sh ./scripts/dev-setup.sh
```

`lima` accepts native Lima settings, such as the four CPU cores and 8 GiB
of memory above. Global and project mappings merge recursively; project scalars
and lists replace inherited values. Overrides apply when creating a VM; use
`--recreate` to apply changes to an existing VM. LJA manages `mounts` and the
VM name. Lima validates other settings.

Configuration must be a single YAML mapping with string keys. Development
settings retain their declared types; `lima` also accepts numeric values,
nested mappings, and lists. Unknown development settings, duplicate keys, nulls,
invalid UTF-8, and multiple documents are errors. YAML aliases in `lima` are
rejected. Configured package names stay native to the guest: Debian
architecture qualifiers are supported, and ordinary Fedora names may contain
uppercase letters and underscores. Options, paths, URLs, globs, and dependency
expressions are rejected. Comments and multiline setup blocks are supported.

The effective defaults are:

```yaml
gpg_forwarding: false
github:
  enabled: false
  token_command: []
lima: {}
packages:
  - git
  - make
agents: []
copy_git_config: true
env: {}
env_passthrough: []
setup: []
```

`packages` and `agents` replace the inherited lists and are deduplicated. The
project `agents` list therefore controls which agents are installed by
default; an explicit empty list clears global defaults. `env` is merged by
name, with the project taking precedence. Setup commands and passthrough names
append to the global values unless a project sets `inherit_setup` or
`inherit_env_passthrough` to `false`. Commands run from the project root in
separate `sh -eu -c` guest processes and may use guest `sudo`. They run for
`shell`, agent launches, `update`, and VM creation; they must be idempotent.
When packages are needed, LJA selects an Ubuntu/Debian or Fedora backend from
guest `/etc/os-release`, checks missing dependencies, installs them in one
backend-specific batch, and verifies them again. Unsupported distributions,
missing package tools, package-database failures, and connection failures are
reported with context. `git` is included automatically when
`copy_git_config` is enabled. Configured default agents are installed or
reused during every preparation, including creation, shell, make, launches,
and update. Shells and multi-agent launches expose configured agents through
per-VM wrappers. An agent launch combines the selected agent, configured
defaults, and any `--with-agent` values in stable deduplicated order.
Recreation runs development setup once before promotion and prepares defaults
on the replacement VM.

When an agent is requested, working guest `node` and `npm` installations are
reused. If either is absent, LJA installs native `nodejs` and `npm` dependencies
through the selected backend and verifies both commands. Present but failing
executables are reported. Global agent installation and update use npm's
strict engine checking; incompatibility errors include runtime versions and
suggest a newer template or custom provisioning. LJA does not install Snap or
add third-party repositories.

Only explicitly named caller variables are forwarded. Missing passthrough
variables are errors, empty values are preserved, and values are never stored
in persistent wrappers or printed by `config`. LJA-managed agent state
variables cannot be overridden by development configuration.

By default Git and Make are prepared in the guest. `copy_git_config` is enabled
by default and can be disabled independently of the package list. Removing a
package from configuration does not uninstall it from an existing VM.

## GitHub authentication

Managed GitHub support is opt-in and currently targets `github.com`. Enable it
in the project file:

```yaml
github:
  enabled: true
```

Supply a token through the host environment or a global token command. LJA
uses the first nonempty source in this order: `GH_TOKEN`, `GITHUB_TOKEN`, then
the command in `${XDG_CONFIG_HOME:-~/.config}/lja/config.yaml`:

```sh
GH_TOKEN="$MY_GITHUB_TOKEN" lja --project ~/src/example codex
```

```yaml
github:
  token_command: [gh, auth, token, --hostname, github.com]
```

The command is global-only, runs directly from the host home directory with no
shell expansion and closed stdin, and has a 30-second timeout. Project files
may override only `github.enabled`; managed support does not need a
`GH_TOKEN` entry in `env_passthrough`. Configuring either token name in
`env` or `env_passthrough` is rejected while support is enabled.

Enabled support installs native guest `git` and `gh` even when `packages` is
empty. Setup, shell, make, and agent commands—including their subprocesses—
receive `GH_TOKEN`; package-manager and agent-install commands do not. Existing
`git@github.com:` and `ssh://git@github.com/` remotes use HTTPS credentials
for that invocation only. LJA does not run persistent GitHub login commands,
edit shared remotes, copy credential stores, or put tokens in Git URLs.

For a fine-grained token, select each target repository and grant only the
permissions needed by the work:

| Work | Repository permission |
| --- | --- |
| Private fetch and repository metadata | `Metadata: read`, `Contents: read` |
| Push commits | `Contents: write` |
| Create or update pull requests | `Pull requests: write` |
| Create or update issues | `Issues: write` |

Organization approval may also be required. See GitHub's [fine-grained token
permission reference](https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens)
for endpoint-specific requirements.

LJA keeps the resolved token only for the current invocation, does not renew or
revoke it, and does not promise process-list secrecy: Lima's environment
transport can expose it through process arguments, while guest commands and
their children can read or retain it. Do not put tokens in YAML, URLs, wrappers,
logs, or credential files. Enterprise hosts, SSH-agent forwarding, token
minting, and automatic renewal are outside the current support.

## GPG access

GPG forwarding is off by default. Enable it globally or in `.lja.yaml`:

```yaml
gpg_forwarding: true
```

LJA installs guest GnuPG, copies public keys into a temporary keyring, and
forwards the host agent while setup and the selected command run. Private keys
stay on the host. Host GnuPG and OpenSSH must be installed, with working host
pinentry. Host `GNUPGHOME` is honored; do not put it in `env` or
`env_passthrough` when forwarding is enabled.

This enables signing **and decryption** with host-agent keys. Guest root can
use the live socket, and host passphrase caching can allow operations without
another prompt. Access ends when LJA exits; background guest jobs then lose it.
Public keys reveal identity metadata, and temporary keyring edits are discarded.
Project `false` overrides global `true`; changes apply to the next invocation.

With Git copying enabled, OpenPGP executable paths are adjusted to guest `gpg`
while signing preferences and key selection are preserved.

## Git and instructions

When Git copying is enabled, LJA reads the host `~/.gitconfig` using Git's own
parser, follows recursive `include.path` and `includeIf.*.path` entries, and
copies `core.excludesFile` files. Host-home-relative paths retain their
guest-home-relative locations. Files outside the home directory are relocated
under `~/.config/lja/git/includes/` and references are rewritten. Host files are
never modified; guest copies are refreshed on preparation. Symlinked guest
destination parents and files are rejected.
Host Git failures include Git's diagnostic message to help resolve setup errors.

On each agent preparation, host-wide instruction files are refreshed into the
selected state root:

| Host source | State destination |
| --- | --- |
| `~/.codex/AGENTS.md` | `.codex/AGENTS.md` |
| `~/.claude/CLAUDE.md` | `.claude/CLAUDE.md` |
| `~/.config/opencode/AGENTS.md` | `.opencode/config/opencode/AGENTS.md` |

Missing sources are optional. Existing destinations are replaced atomically;
other host-agent settings and credentials are not imported.

Codex trust entries for the project and launch directory are maintained in the
selected `.codex/config.toml`. The editor preserves unrelated text and
comments, supports ordinary, dotted-key, and inline project tables, and refuses
malformed or incompatible TOML layouts without writing the file. Login-only
launches do not change trust entries.

## Security boundary and limitations

The VM can modify the mounted project and selected LJA state, execute arbitrary
guest commands, access the network, and use credentials in the selected agent
state. LJA does not provide network isolation, protect project contents, or
forward unrelated host mounts or SSH-agent credentials. Shared state is visible
to every project using that state root, and concurrent interactive use of an
agent's database still needs real-Lima validation.

See [DESIGN.md](DESIGN.md) for implementation guarantees and
[TODO.md](TODO.md) for remaining integration validation.
