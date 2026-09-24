# LJA design

## Purpose

LJA gives a project one persistent Lima VM and a predictable host-side state
layout for coding agents. The host process owns discovery, configuration,
mount validation, locking, and preparation. The guest process owns the agent
runtime and project commands.

The isolation boundary is deliberately narrow: only the selected project and
the selected LJA state root are writable mounts. The VM still has unrestricted
guest execution and ordinary network access. A project and its selected agent
state must therefore be treated as trusted input to the guest.

The Go implementation keeps the workflow reusable as a package and puts the
small executable entry point in `cmd/lja`.

## Package structure

| File | Responsibility |
| --- | --- |
| `path.go` | Canonical paths, VM identity, environment and path safety |
| `config.go` | Layered development configuration and environment resolution |
| `process.go` | Raw host and guest process boundaries |
| `lima.go` | Lima JSON parsing, exact mounts, lifecycle, and bulk operations |
| `lock.go` | Host advisory locks, state roots, directories, and instructions |
| `git.go` | Recursive host Git configuration copying |
| `gpg.go` | Invocation-scoped GPG agent forwarding and revocation |
| `codex.go` | Conservative Codex TOML trust editing |
| `agent.go` | Package provisioning, wrappers, invocation, and launch |
| `discovery.go` | Project discovery, status, and stop |
| `cli.go` | Kong command parsing and dispatch |
| `cmd/lja` | Process entry point |

The package uses `log/slog` for structured diagnostics and returns errors from
every filesystem, process, lock, and encoding operation. User-facing errors
are reported by the CLI with a nonzero status. Agent exit status is propagated
without converting it into a host-side error.

## Command and project selection

The command parser has two kinds of arguments. LJA options and the command name
are parsed by Kong. Agent and shell arguments are retained as a vector and are
forwarded after an optional leading `--` is removed. No host shell is involved
in their interpretation.

The `make` command uses the same project selection, VM preparation, environment,
working-directory, recreation, and exit-status behavior as `shell`, but always
prepends the guest `make` executable. It does not prepare or launch an agent.

`--project` selects one exact directory. Otherwise, the canonical current
directory is retained as the launch directory while discovery checks that
directory and each parent for the nearest `.lja.yaml` or deterministic VM name.
Candidates are checked before inspecting configuration or matching a VM: discovery
stops before the canonical user home directory or a directory not owned by the
process's real user ID. An unsafe starting directory is an error; an unsafe
ancestor ends the search, falling back to the starting directory. Symlinks are
resolved before comparison. Filesystem inspection failures are errors.
Container preparation validates the selected base before acquiring locks and
again before Lima operations, including reuse of running VMs. Explicit project
selection cannot bypass these checks. Explicit status and stop remain available
for existing VMs.
Lima is queried at most once during a discovery walk. A failed query or malformed
response is an error, not an absent VM. A project configuration is not merged
from ancestors after discovery. Git roots are not special.

`stop --all` and `delete --all` are dispatched before project resolution,
configuration loading, or state selection. This makes them usable from outside a project and prevents an
unrelated current directory from affecting them.

## Deterministic identity

The project path is canonicalized to an absolute path with symlinks resolved
where they exist. The VM name is:

```text
lja-<slug>-<hash>
```

The slug lowercases the basename, replaces each run outside ASCII letters and
digits with `-`, truncates to 20 characters, trims edge hyphens, and falls back
to `project`. The hash is the first 12 hexadecimal characters of SHA-256 over
the canonical absolute path. Agent choice, state choice, and additional-agent
requests do not affect identity.

This means a symlink alias has the same VM as its target, while two equal
basenames in different directories do not collide. Moving a project changes
its VM identity; LJA does not adopt similarly named VMs created by another
workflow.

## State and locking

The default state root is `${XDG_DATA_HOME:-~/.local/share}/lja/agents`.
`LJA_STATE_DIR` overrides it, `--state-dir` overrides that, and
`--project-state` selects the project itself. The two storage flags are
mutually exclusive. All selected shared roots are canonicalized and must not
equal, contain, or be contained by the project. Existing non-directories and
empty explicit paths are rejected.

The project and selected state root are mounted at their same absolute paths in
the guest. A host advisory lock is created outside every selected mount and is
held across VM inspection, creation/start, package installation, Git refresh,
instruction synchronization, trust editing, and wrapper setup. It is released
before an interactive agent runs. The lock directory defaults to a temporary
LJA directory, with host cache fallbacks, and can be supplied by the reusable
package API for tests or deployment integration.

Agent state directories are private by default (`0700`). Existing modes are not
silently changed. Every resolved state path is checked again after creation so
symlink escapes are rejected.

## Lima lifecycle

LJA requests `limactl list --all-fields --format json` and accepts both a JSON
array and JSON-lines output. The parser validates instance names, statuses,
configuration objects, mount arrays, mount locations, mount points, and
writable values. Unknown statuses are rejected when an instance is inspected.

For project VM preparation the lifecycle is:

1. Resolve the exact project and selected state root.
2. Calculate the expected canonical writable mount set.
3. Inspect the deterministic VM.
4. If it exists, validate its effective mounts before changing state.
5. Create the second state directory if shared mode needs it.
6. Create an absent VM with `--tty=false`, the managed `--name`, the effective
   native Lima mapping as YAML on stdin, and CSV-encoded `--mount-only` paths.
7. Start a stopped VM, rejecting all other states.
8. Reinspect and revalidate mounts and the running state.
9. Prepare development packages, Git, setup commands, and configured default
   agents.

The expected mount set is exact: no extra mounts, no duplicate entries, no
read-only project/state mount, and no mount at a different guest path. A
mismatch is actionable and never triggers implicit deletion or migration.
Explicit recreation may replace a VM with mismatched mounts without booting it. `status` and
`stop` use the same validation without creating or starting a VM.

`delete` discovers the project and inspects its deterministic VM name without
loading development configuration, resolving storage, or validating mounts. An
absent VM is a successful no-op. Existing VMs are deleted with
`limactl delete -f <name>`, including broken, installing, and uninitialized VMs.
`delete --all` selects the `lja-` namespace, processes VMs sequentially, and
stops on the first failure. Unknown statuses and malformed listings are errors.
Deletion reports `status: Absent` after success and preserves host files and
agent state. Deletion does not acquire LJA advisory locks, matching bulk stop.
Both bulk commands accept `-a`; the former `stop-all` command is removed.

### Create and recreate

`CreateVM(project, WorkflowOptions)` and `lja create` use the same project
lock as other preparation. Existing VMs are returned unchanged after mount
validation when no default agents are configured; this no-op does not resolve
environment passthrough or run setup. If defaults are configured, a stopped
existing VM is started and the configured agents are prepared without rerunning
development setup. An absent VM is created, started, and prepared without
launching an agent; configured default agents are installed or reused. The CLI
reports its name and status.

`WorkflowOptions.Recreate` and the global `--recreate` flag request a fresh
VM. The flag is accepted by create, shell, agent launches, and update, and
rejected by config, status, stop, and delete. A flag after the passthrough
separator belongs to the guest. Every preparation entry point enters the same
replacement flow once; configured-agent installation and trust are prepared on
the candidate, while wrappers are published after promotion under the final VM
name.

If the original is absent, use ordinary creation. Otherwise, reject an
Installing VM, check `limactl rename --help`, allocate unique short
`lja-new-*` and `lja-old-*` names, and log all three names before changing
state. Retain the project lock throughout:

1. Create, boot, validate mounts, and run development and configured-agent
   preparation on the temporary VM using the current configuration and selected
   host state. Defer wrapper publication until the replacement has its final
   VM name.
2. Stop the temporary VM, then stop the original if it was running.
3. Rename the original to the backup name and the temporary VM to the
   deterministic project name, with `--tty=false`.
4. Start the replacement under its final name, revalidate mounts and Running
   status, and probe guest connectivity.
5. Delete the backup guest disk, publish configured-agent wrappers under the
   final VM name, and report cleanup or wrapper failures without rolling back
   the working replacement. Continue the requested workflow without repeating
   development setup.

Preparation failures leave the original untouched and clean up the temporary
VM when inspection permits; all cleanup failures are returned with the primary
error. If final startup or validation fails after both renames succeeded,
stop the replacement, rename it aside, restore the backup name, and restore
the original's running state. Delete the failed replacement only after
restoration succeeds.

Lima rename moves files individually and is not atomic. A rename error may
leave both directories partially populated, so no automatic deletion or
further rename is attempted after such an error. Return the original,
temporary, and backup names with recovery instructions. Rollback failures
likewise retain recoverable VMs and report their names. Interrupted operations
can leave temporary or backup instances; LJA does not automatically adopt or
delete them. They remain visible to Lima and the existing `--all` lifecycle
commands, so recovery should precede bulk deletion.

Recreation does not migrate guest disks or host state. Setup can change the
shared project and agent-state files while the original is still running;
VM rollback cannot undo these changes.

## Process boundary

Host commands are constructed as argument vectors and executed with
`os/exec`. Lima shell calls are similarly vectors:

```text
limactl shell [--tty=false] --workdir PROJECT VM [NAME=VALUE...] COMMAND...
```

For example, `lja make -- test` invokes `make test` directly through this
argument-vector boundary; it does not run a host or guest shell to interpret
the make arguments.

Environment names are sorted for deterministic calls, validated against the
portable shell variable pattern, and checked for NUL values. Setup commands
use `sh -eu -c` as separate guest processes. Interactive commands inherit
standard streams. Captured probes distinguish a missing command from a failed
guest connection and include output in failures without logging arbitrary
environment values or prompts.

Every guest command is run through an invocation-scoped POSIX bootstrap that
preserves the guest `PATH` and prepends `$HOME/.local/bin`,
`$HOME/go/bin`, `PIPX_BIN_DIR`, `GOBIN`, and the first
`GOPATH` entry's `bin` directory when present. If Go is
available, its effective `GOBIN` or `GOPATH` from
`go env` is also added. This applies equally to direct commands,
setup, package and executable probes, GPG operations, interactive shells, and
agent launches. Host paths are never copied into the guest environment, and
guest profiles are not modified.

## Development configuration

The global file is `${XDG_CONFIG_HOME:-~/.config}/lja/config.yaml`; the project
file is `.lja.yaml` directly below the selected project. Both are optional
single-document YAML mappings decoded with
[go.yaml.in/yaml/v3](https://pkg.go.dev/go.yaml.in/yaml/v3). Legacy JSON
filenames are ignored and do not select a project. Lima's JSON protocol is
unaffected.

Node validation requires string keys, string values in `env`, booleans for
boolean settings, and sequences of strings for list settings. Unknown and
duplicate keys, null values, invalid types, invalid UTF-8, and multiple YAML
documents are rejected. Errors include the source filename when loading a file
and include a line and column whenever the YAML node has that information.
Invalid package names, environment names, NUL values, and variables managed
by LJA are also rejected. Missing files retain defaults, while an explicit empty mapping
is valid.

The effective defaults are:

```yaml
gpg_forwarding: false
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

The `lima` setting is a native Lima YAML mapping, defaulting to `{}`.
It supports strings, booleans, finite numbers, lists, and nested string-keyed
mappings. Recursive validation rejects duplicate keys, nulls, aliases, custom
scalar tags, and NUL characters with source locations. The top-level
`lima.mounts` key is reserved for LJA. Other Lima keys and their semantics
are validated by Lima during creation.

Global and project Lima mappings merge recursively. Project scalars and lists
replace inherited values; empty mappings contribute no overrides to inherited
mappings. Configuration cloning deep-copies mappings and lists. `lja config`
includes the merged overrides, not a resolved Lima template.

Creation passes the effective top-level Lima mapping as one YAML document on
stdin to `limactl create ... -`; a missing `base` and `images` pair gets
`base: template:default` in that creation-only document. Explicit empty values
remain explicit, and the configured mapping and `lja config` output are not
changed. Lima resolves relative template references from the project working
directory and handles external template references itself. No shell evaluates
configuration values. The managed VM name and exact mounts remain under LJA
control, and effective mounts are checked before boot. Existing VMs are never
edited to apply resource changes: explicit recreation is required.

Example project overrides:

```yaml
lima:
  cpus: 4
  memory: "8GiB"
```

Project package and agent lists replace the inherited lists and are deduplicated.
An explicit empty `agents` list clears global agent defaults. Explicit
environment values merge by name. Setup commands and passthrough names append
unless the project disables inheritance. Each setup entry records its source
and one-based command index for failure reporting. YAML comments and literal
block strings are accepted; each block remains one setup entry.

Caller environment values are read only when an operation needs to prepare a
VM. A passthrough name must exist, while an explicit `env` value takes
precedence and may intentionally be empty. The resolved values are sent only
to setup and the selected guest command. They are not written to wrappers,
configuration output, or LJA logs. `config`, `status`, `stop`, and a no-op `create` do not require
passthrough variables; bulk stop and delete skip development configuration entirely.

`lja config` will emit deterministic YAML with two-space indentation and a
trailing newline, including empty collections and the existing effective
settings. It will not resolve passthrough values or include setup provenance.
Configuration loading never rewrites source files, and the exported project
filename constant is `.lja.yaml`.

## Guest preparation and agents

When development packages are needed, LJA reads the guest `/etc/os-release`
and selects the Ubuntu/Debian or Fedora backend. It verifies the selected
backend's query, install, and `sudo` tools before checking dependencies. The
Ubuntu/Debian backend uses `dpkg-query`, then batches missing packages through
`sudo apt-get update && sudo apt-get install -y PACKAGE...`; the Fedora backend
uses `rpm -q --whatprovides PACKAGE` and `sudo dnf install -y PACKAGE...`.
Each missing dependency is checked again after installation. Unsupported guest
distributions, missing tools, package-database failures, and guest connection
failures are reported with backend, VM, and dependency context. Agent
installation is independent of the configured development package list.

The shared Node recipe is:

```text
sudo snap install node --classic
sudo npm install -g PACKAGE
```

The selected agent executable is probed before installation and after package
installation. Existing installations are reused; `update` explicitly installs
the selected package again. The built-in mappings are:

| Agent | Package | Executable | Default |
| --- | --- | --- | --- |
| Codex | `@openai/codex` | `codex` | `--dangerously-bypass-approvals-and-sandbox` |
| Claude Code | `@anthropic-ai/claude-code` | `claude` | `--dangerously-skip-permissions` |
| OpenCode | `opencode-ai` | `opencode` | `OPENCODE_CONFIG_CONTENT={"permission":"allow"}` |

Login commands are forwarded without permission defaults. Explicit native
permission options are detected in both `--option value`-style vectors and
`--option=value` forms. Codex's `exec` subcommand gets its default after the
subcommand.

Configured default agents are installed or reused during every preparation,
including create, shell, make, agent launches, and update. They receive state
directories, instruction refreshes, and Codex trust preparation. Shells and
multi-agent launches expose them through per-VM wrappers in
`/tmp/lja/<vm>/bin`. The launch prepends that directory to `PATH`; each wrapper
restores only its own state environment and permission behavior, so an agent
can invoke another by its ordinary guest executable name. Agent launches
combine the selected agent, configured defaults, and `--with-agent` values in
stable deduplicated order.

## Planned: Fedora guests and native dependencies

This section describes future behavior, not implemented support. The current
Lima creation and guest preparation behavior above remains authoritative until
the tasks in [TODO.md](TODO.md#fedora-guests-and-native-dependencies) are complete.
No additional distro, package-manager, or Node-provider settings are planned.

### Template selection

Select Fedora through the existing native Lima mapping:

```yaml
lima:
  base: template:fedora
packages:
  - git
  - make
  - gcc
```

Lima provides the [Fedora template](https://lima-vm.io/docs/templates/).
Creation will serialize the effective `lima` mapping as YAML and pass it on
stdin to `limactl create ... -`, replacing the current `--set` transport.
This is necessary because Lima expands template bases before applying those
command-line overrides. When both `base` and `images` are absent, LJA will
inject `base: template:default` into creation input only; `lja config` will
continue reporting configured values rather than expanded template defaults.
Explicit empty values will not count as absent.

Global/project configuration will retain its current merge rules. Lima will
then apply its own template inheritance semantics to the merged input. Relative
local template references in that input will resolve from the selected project
directory. References inside an external template remain relative to that
template according to Lima's rules. LJA will continue rejecting `lima.mounts`,
applying its exact mount set through `--mount-only`, and validating effective
mounts before boot.

Template changes will apply only during creation or explicit recreation.
Existing guests will use their actual installed distribution for preparation,
regardless of the currently configured template. No new public configuration
fields or CLI options are needed.

### Guest package backends

When package preparation is needed, LJA will read `/etc/os-release` in the
guest and select an internal backend for Ubuntu/Debian or Fedora. It will
verify the required package tools and report unsupported distributions or
missing tools explicitly, without guessing from the configured template or
falling back to a different package manager.

| Behavior | Ubuntu/Debian | Fedora |
| --- | --- | --- |
| Query installed dependencies | `dpkg-query` | RPM queries, including provided capabilities |
| Install missing dependencies | `sudo apt-get update`, then `sudo apt-get install -y` | `sudo dnf install -y` |
| Default development tools | `git`, `make` | `git`, `make` |
| Automatic Git dependency | `git` | `git` |
| Automatic GPG dependency | `gnupg` | `gnupg2` |
| Node runtime dependencies | `nodejs`, `npm` | `nodejs`, `npm` capabilities |

The existing `packages` list will contain native package names for the chosen
guest. Replacement, deduplication, and explicit-empty behavior will remain;
only LJA-owned dependencies will be translated between distributions. Package
validation will accept ordinary RPM names, including uppercase letters and
underscores, while retaining Debian architecture qualifiers. Options, paths,
URLs, globs, and arbitrary dependency expressions will remain disallowed.
Packages will remain safely quoted or passed as arguments without shell
interpretation.

Each preparation phase will query dependencies, install missing packages in
one batch, and verify the result. Query handling must distinguish absence from
connection failures and package-database errors. Installation failures must
include the VM, backend, and requested dependencies in their context. The
backend will also verify required executables, including `gpg` and `gpgconf`
for forwarding, rather than treating package presence as sufficient.

Development dependencies will still precede Git preparation and setup; setup
will still precede agent installation. Existing `lima.provision` is the escape
hatch for preparing repositories before development package installation.
Existing setup commands can prepare a custom system-wide Node runtime before
agent installation. Host-side `make dep` changes are outside this design.

### Node and agents without Snap

Ubuntu provides an [npm APT package](https://packages.ubuntu.com/noble/npm)
that depends on Node.js. Fedora can satisfy the unversioned runtime requests
through versioned packages and their
[provided capabilities](https://packages.fedoraproject.org/pkgs/nodejs22/nodejs22/fedora-44.html).
The backend must query those capabilities instead of assuming an installed
RPM is literally named `nodejs` or `npm`.

LJA will reuse working `node` and `npm` executables. If either is absent, it
will install the native runtime dependencies through the detected backend and
verify both executables afterward. A present but failing executable is an
error, not permission to silently replace a custom runtime. Automatic Snap
installation and fallback will be removed entirely; pre-existing Snap
installations will not be uninstalled.

Agent packages will still be installed globally through npm. Install and
update will enforce declared engine requirements with
`sudo npm install --engine-strict -g PACKAGE`. Incompatibility errors will
report the runtime versions and suggest selecting a newer template or
provisioning a suitable system-wide runtime through existing configuration.
LJA will not automatically add third-party repositories or select a different
runtime provider. Native package availability does not guarantee compatibility
with every future agent release; successful agent executable probes remain
required after installation. Existing-agent reuse remains unchanged.

## Instructions and Codex trust

Instruction synchronization is one-way and atomic. Optional host sources are:

```text
~/.codex/AGENTS.md       -> <state>/.codex/AGENTS.md
~/.claude/CLAUDE.md      -> <state>/.claude/CLAUDE.md
~/.config/opencode/AGENTS.md -> <state>/.opencode/config/opencode/AGENTS.md
```

Explicit host configuration-directory environment overrides are honored when
locating sources. Missing sources leave existing copies unchanged. Destination
symlinks, non-regular files, and paths resolving outside the state root are
errors. Temporary files are fsynced before rename.

Codex trust editing uses the pinned
`github.com/pelletier/go-toml/v2/unstable/edit` document editor. It supports
ordinary project tables, dotted keys, and inline tables through semantic key
paths while preserving unrelated text, comments, whitespace, ordering, and
line endings. It rejects malformed TOML, duplicate definitions, incompatible
target types, and unsupported edits before any write. Do not replace the
document with a general decode-and-marshal round trip.

Keep `CodexTrustedConfig`'s signature and canonicalize and deduplicate target
directories as today. Address each trust setting using the separate key
segments `projects`, the literal canonical directory, and `trust_level` so
dots or quotes in a path cannot change its meaning. Change an existing trust
value to `trusted`, insert a missing setting, or create a missing project
entry. An already trusted entry must not cause an edit.

Unrelated valid TOML, including multiline strings and arrays, survives
unchanged. Comments, spacing, line endings, and ordering are preserved outside
the edited value or necessary insertion. New content follows the document's
line endings, defaulting to LF for a new file. Repeating an update produces
identical bytes and avoids a file write.

Parse and validate the whole input and resulting document before publishing
any change. Malformed TOML, duplicate definitions, incompatible target types,
and unsupported edits must return an actionable error without writing. Keep
the existing accepted trust values (`trusted` and `untrusted`); do not silently
replace an unexpected value or a scalar where a project table is required.
The existing state-root lock, atomic replacement, file mode preservation,
owner-only new files, path safety checks, and login-only behavior remain part
of the contract. The host's separate Codex configuration is not edited.

## Git configuration

Host Git failures report the config path, exit code, and nonempty stderr
diagnostics. Configuration output on stdout is kept separate from errors.

Host Git is queried with `--no-includes`; LJA follows the root `.gitconfig`, all
`include.path` and `includeIf.*.path` references, and `core.excludesFile` values
it discovers. Relative references resolve against their source file. Host-home
files retain guest-home-relative paths. Outside-home files are mapped below:

```text
~/.config/lja/git/includes/<sha256-host-path>/<basename>
```

References in copied configs are rewritten to those guest paths. Include cycles,
depth beyond 128, destination collisions, empty paths, unsupported Git path
forms, malformed files, and transfer failures are errors. Missing optional
referenced files are logged at debug level and do not invalidate the rest of
the copy set. Content is transferred through stdin to a guest script that
creates private parents, rejects symlinks, and atomically replaces each file.
Included files are published before the root `.gitconfig`, so a retry can
complete a partial transfer. The host configuration is never modified.

## Planned: GitHub authentication

This section describes future behavior, not implemented support. The tasks in
[TODO.md](TODO.md#github-authentication) cover implementation. Existing manual
environment passthrough, package configuration, and setup remain available.
Managed support initially targets `github.com`; Enterprise hosts, SSH-agent
forwarding, token minting, and automatic renewal are outside this feature.

### Configuration and token resolution

The planned defaults are:

```yaml
github:
  enabled: false
  token_command: []
```

Global and project configuration may set `github.enabled`, with project values
overriding global values. Only global configuration may define
`github.token_command`, because it executes on the host outside the VM. Reject
that key in project configuration even when empty or disabled. The command is
an argument vector; an empty list disables command fallback. Example global
configuration for explicitly using an existing host GitHub CLI login:

```yaml
github:
  token_command: ["gh", "auth", "token", "--hostname", "github.com"]
```

A project enables support with `github: {enabled: true}`. No additional
`env_passthrough` entry is needed. When enabled, resolve the first nonempty host
environment value in order: `GH_TOKEN`, then `GITHUB_TOKEN`. If neither exists,
run the explicitly configured token command; otherwise report a missing-token
error. There is no implicit host credential-store lookup. This environment
precedence follows the
[GitHub CLI environment contract](https://cli.github.com/manual/gh_help_environment).
Reject `GH_TOKEN` and `GITHUB_TOKEN` entries in configured `env` or
`env_passthrough` while managed support is enabled, avoiding competing sources.
Disabled support preserves existing manual behavior.

Execute the command directly without shell expansion, from the host home
directory, with closed stdin and a 30-second timeout. Resolve once per LJA
invocation, including recreation, and retain the result only in memory. Accept
one nonempty token line with an optional trailing LF or CRLF; reject embedded
newlines and control characters. Apply the same token validation to environment
values, without treating malformed values as permission to try another source.
Command failure, timeout, or cancellation is an error. Diagnostics identify
the executable and exit status or termination reason without printing captured
stdout, stderr, or command arguments that might contain secrets.

Resolve credentials only for operations that prepare or execute guest work.
Configuration inspection, lifecycle-only commands, and an existing-VM no-op
create do not require tokens or invoke the command. `lja config` will show the
effective GitHub settings and command definition, never the resolved token;
command arguments should name a credential source rather than embed a token.
The next invocation resolves afresh, without persistent token caching or
automatic refresh during an invocation.

### Guest dependencies and Git integration

Enabled support will automatically require `git` and `gh`, including when
`packages` is empty, through the planned native package backends. Ubuntu and
Fedora provide native `gh` packages; Node and npm are not dependencies of this
integration. See the [Ubuntu package](https://packages.ubuntu.com/noble/gh) and
[Fedora package](https://packages.fedoraproject.org/pkgs/gh/gh/).

Prepare invocation-scoped authentication after ordinary Git configuration
copying and before project setup. This also works with `copy_git_config: false`.
Supply the selected token as `GH_TOKEN` to setup, shell/make, and agent commands;
their subprocesses, including nested agents, inherit it. Package-manager
commands do not need the token. Do not run an unconditional GitHub API probe
during preparation: actual GitHub operations report expired credentials,
insufficient permissions, and network failures.

Use Git's `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>`, and
`GIT_CONFIG_VALUE_<n>` environment entries for temporary runtime settings.
Preserve valid existing runtime entries and append managed entries; reject
malformed existing entries instead of discarding them. For
`credential.https://github.com.helper`, append an empty value to reset inherited
helpers, followed by `!gh auth git-credential`. This provides the relevant
credential integration without persistent `gh auth setup-git` changes. See
[Git runtime configuration](https://git-scm.com/docs/git-config) and
[GitHub's credential integration](https://cli.github.com/manual/gh_auth_setup-git).

Append invocation-scoped `url.https://github.com/.insteadOf` entries for
`git@github.com:` and `ssh://git@github.com/`. These allow existing SSH remotes
to use HTTPS authentication without editing shared repository remotes or host
configuration. Verify interaction with copied credential helpers and URL
rewrites; report conflicting rules that prevent the intended HTTPS transport
instead of silently attempting SSH. Other Git hosts retain their configuration.

Do not run persistent `gh auth login`, copy host GitHub credential stores, or
place tokens in Git URLs. Setup and launched commands share the invocation's
token and Git settings; concurrent invocations must not overwrite one another's
credentials or Git authentication settings. Disabling support on a subsequent
invocation requires no persistent Git configuration cleanup.

### Token permissions and lifetime

Document repository-scoped token permissions according to the desired work:
Contents read for fetch and write for push, Pull requests for PR operations,
and Issues when needed. Organization approval requirements still apply. Link
to GitHub's [token permission reference](https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens)
rather than assuming one token scope grants every GitHub CLI operation.

LJA must not write resolved tokens into configuration output, wrappers, logs,
or credential files. The current Lima environment transport places values in
process arguments; this design retains that transport and does not promise
process-list secrecy. Guest commands receive a usable bearer token and can
retain it. Ending LJA does not revoke the token or erase copies retained by
guest processes. Unlike GPG forwarding, this is credential delivery rather than
an invocation-limited connection to a host credential service.

## GPG forwarding

`gpg_forwarding` is a boolean development setting, defaulting to `false`.
Global and project configuration follow ordinary scalar precedence, so project
`false` overrides global `true`. `DevelopmentConfig.GPGForwarding` exposes the
same setting to package callers, and `lja config` includes its effective value.
Enabling it in project YAML grants the project access to host GPG operations.
No VM recreation is needed; changes affect subsequent invocations.

Each enabled workflow owns a temporary forwarding session. Development setup
and the selected shell, make, or agent command share its `GNUPGHOME`; nested
agents inherit it. Preparation-only calls, including create and update, close
forwarding before returning. Existing-VM no-op create, config, status, stop,
and delete do not discover GPG, export keys, or start forwarding. Disabled
workflows perform none of these operations. Configured `GNUPGHOME` values or
passthrough are rejected when forwarding is enabled; host `GNUPGHOME` still
selects the source keyring and agent.

After development packages and Git configuration are prepared, LJA:

1. Requires host OpenSSH and GnuPG, starts the host agent with
   `gpgconf --launch gpg-agent`, and discovers `agent-extra-socket`. The socket
   must exist and be a Unix socket. There is no unrestricted-socket fallback.
2. Exports public keys with `gpg --batch --export`. Creates a unique `0700`
   guest directory under `/tmp/lja-gpg-*`, writes `no-autostart` to its GPG
   configuration, discovers its agent socket, and creates a runtime socket
   directory only when GnuPG selects one outside the temporary home. Public
   keys are imported through stdin; an empty host public keyring is valid.
3. Creates a private host Unix socket proxy in a unique temporary directory.
   The proxy connects only to the discovered host extra socket and tracks all
   active connections for revocation.
4. Queries Lima for the instance's `SSHConfigFile` and starts a dedicated SSH
   Unix-socket reverse forward from the guest agent socket to the proxy.
   Connection sharing, SSH-agent forwarding, and X11 forwarding are disabled;
   forwarding failures are fatal. Existing socket paths are never unlinked
   to establish a tunnel. SSH keepalives detect disconnected peers.
5. Waits for the guest socket and checks an agent `GETINFO version` response
   before running setup or the requested command. Startup has a 30-second
   timeout. GPG protocol traffic is never logged, and session environment values are not
   put in persistent wrappers.

The restricted extra socket is GnuPG's intended remote forwarding interface:
see [GnuPG agent options](https://www.gnupg.org/documentation/manuals/gnupg/Agent-Options.html).
Transport uses [OpenSSH Unix socket reverse forwarding](https://man.openbsd.org/ssh.1).
Host pinentry and passphrase caching remain under host control. A GUI pinentry
is convenient; terminal pinentry must already have a usable host terminal.
LJA does not transmit passphrases or guest terminal settings to the host agent.

The workflow closes all proxy connections first, then terminates and reaps
SSH and removes its temporary directories and GnuPG runtime socket directory.
Cleanup errors are returned alongside workflow errors; guest cleanup has a
five-second timeout with one additional second to close inherited process
pipes if descendants retain them. SIGINT, SIGTERM, proxy failures, and unexpected tunnel
exit cancel the active setup or command and revoke access. Even SIGKILL closes
the in-process proxy descriptors, so an orphaned SSH process cannot retain
host-agent access. Abrupt termination can leave inert temporary files and
orphaned processes; normal cleanup cannot run after SIGKILL.

Concurrent invocations use independent homes, sockets, and tunnels. Recreation
closes the candidate VM's session before stopping or renaming it, and launches
establish a fresh session for the final VM without rerunning setup. Public
keyring changes made by setup in the candidate session are discarded along
with that session.

Private keys, ownertrust, and host GPG configuration are never copied or
mounted. Exported public keys do reveal identity metadata. The extra socket
permits signing and decryption using agent-accessible keys; it is not a
signing-only interface or a per-key allowlist. Guest root can use any live
session socket. Separate sessions prevent collisions, not access by other
privileged guest processes. Temporary keyring changes are discarded at exit.

When Git configuration copying is also enabled, `gpg.program` and
`gpg.openpgp.program` are rewritten to guest `gpg` in the root and recursively
included copies. Signing preferences, signing key selection, and SSH/X.509
program settings retain their original values. Host files are never edited.

## Validation strategy

The repository uses `gotest.tools/v3` assertions and table-oriented tests for
the pure workflow pieces. Tests cover canonical identity, YAML/configuration
validation, exact mount logic, state precedence, process argument boundaries,
Codex editing, instruction replacement, Git path rewriting, wrappers, and
failure handling. A fake `limactl` command can be supplied through
`WorkflowOptions.LimaCommand` for process-boundary and lifecycle tests.

`make check` runs `prek run --all-files`, `go test ./...`, and `go build ./...`.
The repository hook formats Go files and runs `go vet ./...`.

Routine tests do not boot Lima or use agent accounts. GPG tests use fake Lima
and SSH processes with live Unix sockets, including active-connection
revocation after SIGTERM and SIGKILL. `make test-gpg` additionally generates a
disposable local keyring to verify signing, decryption, public-only transfer,
and revocation against a real GnuPG agent; it never uses the caller's keyring. Real-Lima validation is
still needed for agent database persistence and concurrent shared-state use;
the concrete checklist is in [TODO.md](TODO.md).
