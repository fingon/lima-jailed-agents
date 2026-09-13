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
| `lima.go` | Lima JSON parsing, exact mounts, lifecycle, and stop-all |
| `lock.go` | Host advisory locks, state roots, directories, and instructions |
| `git.go` | Recursive host Git configuration copying |
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

`--project` selects one exact directory. Otherwise, the canonical current
directory is retained as the launch directory while discovery checks that
directory and each parent for the nearest `.lja.json` or deterministic VM name.
Lima is queried at most once during a discovery walk. A failed query or malformed
response is an error, not an absent VM. A project configuration is not merged
from ancestors after discovery. Git roots are not special.

`stop-all` is dispatched before project resolution, configuration loading, or
state selection. This makes it usable from outside a project and prevents an
unrelated current directory from affecting it.

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

For a project operation the lifecycle is:

1. Resolve the exact project and selected state root.
2. Calculate the expected canonical writable mount set.
3. Inspect the deterministic VM.
4. If it exists, validate its effective mounts before changing state.
5. Create the second state directory if shared mode needs it.
6. Create an absent VM with `--tty=false`, `--name`, and CSV-encoded
   `--mount-only` paths.
7. Start a stopped VM, rejecting all other states.
8. Reinspect and revalidate mounts and the running state.
9. Prepare development packages, Git, and setup commands.

The expected mount set is exact: no extra mounts, no duplicate entries, no
read-only project/state mount, and no mount at a different guest path. A
mismatch is actionable and never triggers deletion or migration. `status` and
`stop` use the same validation without creating or starting a VM.

## Process boundary

Host commands are constructed as argument vectors and executed with
`os/exec`. Lima shell calls are similarly vectors:

```text
limactl shell [--tty=false] --workdir PROJECT VM [NAME=VALUE...] COMMAND...
```

Environment names are sorted for deterministic calls, validated against the
portable shell variable pattern, and checked for NUL values. Setup commands
use `sh -eu -c` as separate guest processes. Interactive commands inherit
standard streams. Captured probes distinguish a missing command from a failed
guest connection and include output in failures without logging arbitrary
environment values or prompts.

## Development configuration

The global file is `${XDG_CONFIG_HOME:-~/.config}/lja/config.json`; the project
file is `.lja.json` directly below the selected project. Both are optional JSON
objects. Unknown keys and invalid types are rejected, as are package options,
invalid environment names, NUL values, and variables managed by LJA.

The effective defaults are:

```json
{
  "packages": ["git", "make"],
  "copy_git_config": true,
  "env": {},
  "env_passthrough": [],
  "setup": []
}
```

Project package lists replace the inherited list and are deduplicated. Explicit
environment values merge by name. Setup commands and passthrough names append
unless the project disables inheritance. Each setup entry records its source
and one-based command index for failure reporting.

Caller environment values are read only when an operation needs to prepare a
VM. A passthrough name must exist, while an explicit `env` value takes
precedence and may intentionally be empty. The resolved values are sent only
to setup and the selected guest command. They are not written to wrappers,
configuration output, or LJA logs. `config`, `status`, and `stop` do not require
passthrough variables; `stop-all` skips development configuration entirely.

### Planned: YAML configuration

This change is not implemented yet; the JSON behavior above remains current.
Implementation steps are tracked in [TODO.md](TODO.md).

The global file will be `${XDG_CONFIG_HOME:-~/.config}/lja/config.yaml` and the
project file will be `.lja.yaml`. Discovery will use `.lja.yaml` as its file
marker. Only the `.yaml` filenames will be recognized; legacy JSON filenames
will neither be loaded nor select a project. This prototype needs no migration
tooling or compatibility fallback. Lima's JSON protocol is unaffected.

Both optional files will contain a single YAML mapping, decoded with
[go.yaml.in/yaml/v3](https://pkg.go.dev/go.yaml.in/yaml/v3). Node validation will
enforce string keys, string values in `env`, boolean settings, and sequences
of strings without implicit scalar coercion. Unknown and duplicate keys, null
values, invalid UTF-8, and multiple documents will be errors. Existing package,
environment, and project-only inheritance validation will remain in effect.
Missing files retain defaults; an explicit empty mapping is valid.

Defaults and merge semantics will remain unchanged, including the distinction
between omitted settings and explicit empty collections. Comments and literal
block strings will be accepted, allowing a project configuration such as:

```yaml
# Packages replace the inherited list.
packages: [git, make, ninja-build]
copy_git_config: true
env:
  BUILD_MODE: development
  BUILD_COUNT: "3" # Environment values must be strings.
env_passthrough: [HTTPS_PROXY]
inherit_setup: false
setup:
  - |
    make dep
    make build
```

Each block remains one setup entry, executed by the existing `sh -eu -c`
workflow with its source path and one-based entry index. Configuration loading
will never rewrite source files.

`lja config` will emit deterministic YAML with two-space indentation and a
trailing newline, including empty collections and the existing effective
settings. It will not resolve passthrough values or include setup provenance.
The JSON-specific output representation and `AsJSON` helper will be replaced
with a YAML representation; the loading and validation API signatures remain
unchanged, and the exported project filename constant changes to `.lja.yaml`.

## Guest preparation and agents

Development packages are checked with `dpkg-query`. Missing packages are
installed with `sudo apt-get update` and `sudo apt-get install -y PACKAGE`,
then checked again. Agent installation is independent of the configured
development package list.

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

Additional agents are normalized and deduplicated with the selected agent. All
requested agents receive installation checks, state directories, instruction
refreshes, and Codex trust preparation. When more than one is requested, LJA
creates per-VM wrappers in `/tmp/lja/<vm>/bin`. The launch prepends that
directory to `PATH`; each wrapper restores only its own state environment and
permission behavior, so an agent can invoke another by its ordinary guest
executable name.

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

Codex trust editing is intentionally conservative because the Go module does
not need a general TOML dependency. It supports ordinary
`[projects."absolute path"]` tables and single-line assignments while
preserving unrelated text and comments. It rejects inline/dotted project
definitions, nested or duplicate project tables, duplicate trust settings,
multiline values, and unsupported escapes before any write. Existing file modes
are preserved; a new file is owner-only (`0600`). A state-root lock serializes
read-modify-write updates, and an atomic temporary-file replacement avoids
publishing partial configuration.

### Planned: library-based Codex TOML editing

The editor above remains the current implementation. The planned replacement
will use `github.com/pelletier/go-toml/v2/unstable/edit`, subject to preservation
tests before adoption. Its upstream [document editing documentation](https://github.com/pelletier/go-toml#document-editing)
describes changes that preserve comments, whitespace, ordering, and untouched
bytes. Pin the dependency because this editing API is explicitly unstable.
Do not replace the document with a general decode-and-marshal round trip.

Keep `CodexTrustedConfig`'s signature and canonicalize and deduplicate target
directories as today. Address each trust setting using the separate key
segments `projects`, the literal canonical directory, and `trust_level` so
dots or quotes in a path cannot change its meaning. Change an existing trust
value to `trusted`, insert a missing setting, or create a missing project
entry. An already trusted entry must not cause an edit.

Support ordinary project tables, dotted keys, and inline tables through the
library's semantic key lookup. Unrelated valid TOML, including multiline
strings and arrays, must survive unchanged. Preserve comments, spacing, line
endings, and ordering outside the edited value or necessary insertion. New
content should follow the document's line endings, defaulting to LF for a new
file. Repeating an update must produce identical bytes and avoid a file write.

Parse and validate the whole input and resulting document before publishing
any change. Malformed TOML, duplicate definitions, incompatible target types,
and unsupported edits must return an actionable error without writing. Keep
the existing accepted trust values (`trusted` and `untrusted`); do not silently
replace an unexpected value or a scalar where a project table is required.
If the library cannot meet preservation requirements, leave its adoption
pending in TODO.md rather than falling back to whole-document formatting.

The existing state-root lock, atomic replacement, file mode preservation,
owner-only new files, path safety checks, and login-only behavior remain part
of the contract. The host's separate Codex configuration is not edited.

## Git configuration

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

## Validation strategy

The repository uses `gotest.tools/v3` assertions and table-oriented tests for
the pure workflow pieces. Tests cover canonical identity, JSON/configuration
validation, exact mount logic, state precedence, process argument boundaries,
Codex editing, instruction replacement, Git path rewriting, wrappers, and
failure handling. A fake `limactl` command can be supplied through
`WorkflowOptions.LimaCommand` for process-boundary and lifecycle tests.

`make check` runs `prek run --all-files`, `go test ./...`, and `go build ./...`.
The repository hook formats Go files and runs `go vet ./...`.

Routine tests do not boot Lima or use agent accounts. Real-Lima validation is
still needed for agent database persistence and concurrent shared-state use;
the concrete checklist is in [TODO.md](TODO.md).
