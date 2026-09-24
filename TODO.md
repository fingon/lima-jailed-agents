# Implementation backlog

## Fedora guests and native dependencies

The target behavior is described in
[Planned: Fedora guests and native dependencies](DESIGN.md#planned-fedora-guests-and-native-dependencies).
This feature is not implemented. Current runtime behavior and README support
claims remain unchanged until implementation.

- [ ] Replace Lima `--set` creation overrides with the effective native Lima
  mapping serialized as YAML on stdin to `limactl create ... -`. Inject
  `base: template:default` only when both `base` and `images` are absent,
  without changing `lja config` output. Preserve global/project merging,
  resolve input-relative template paths from the project, and leave external
  template references to Lima. Preserve managed names, `--mount-only`, exact
  mount validation, and candidate preparation during recreation.
- [ ] Add internal Ubuntu/Debian and Fedora package backends selected from
  guest `/etc/os-release` when needed. Verify tools, reject unsupported guests,
  and use dpkg/APT or RPM/DNF respectively. Query RPM provided capabilities,
  batch missing dependencies per phase, and verify installation. Distinguish
  missing packages from database and connection failures; propagate errors
  with VM, backend, and dependency context.
- [ ] Map LJA-owned Git, GPG, and Node dependencies to the selected backend;
  keep configured `packages` native and retain defaults, replacement, and
  deduplication. Verify required executables, including `gpg` and `gpgconf`.
  Accept ordinary RPM names with uppercase letters and underscores while
  preserving Debian architecture qualifiers. Reject options, paths, URLs,
  globs, and arbitrary dependency expressions.
- [ ] Remove automatic Snap installation and fallback. Reuse working Node/npm
  installations; install native `nodejs`/`npm` dependencies when either is
  missing and verify both commands. Report present but failing executables.
  Enforce npm engine requirements during global agent install/update and
  provide runtime-version context and newer-template/custom-provisioning
  guidance on incompatibility. Preserve existing-agent reuse and do not
  uninstall pre-existing Snap runtimes or add third-party repositories.
- [ ] Add table-driven tests with `gotest.tools/v3` and golden fixtures in
  `testdata/` for Fedora/default template input, inherited configuration,
  absent versus empty base/images, custom images, relative references,
  creation/recreation stdin transport, and exact mount arguments. Verify
  existing guests use their actual distribution despite template changes.
- [ ] Test both backends for installed/missing dependencies, RPM capability
  providers, invalid package names, unsupported distributions, missing tools,
  failed connections, query failures, install failures, and post-install
  verification. Cover GPG dependency mapping and unchanged setup ordering.
- [ ] Test absent/partial runtimes, working custom runtime reuse, failing
  executables, incompatible engines, agent reuse and update, and absence of
  Snap commands. Ensure shell/make without agents do not install Node unless
  requested through `packages`.
- [ ] Validate disposable Ubuntu and Fedora VMs: create, shell/make, configured
  dependencies, each built-in agent's installation/update, GPG forwarding,
  repeat preparation, and recreation. Check `node` and `npm` are runnable,
  including Fedora's unversioned executable providers. Record tested Lima and
  guest versions. Live validation requires access to Lima's host state;
  the planning session could not access `~/.lima` through its nono sandbox.
- [ ] At implementation time, update README examples and support claims and
  promote the planned DESIGN.md section to current behavior, replacing the
  superseded creation and provisioning descriptions. Enable the prek hook,
  run `make lint`, `make test`, and `make build`, and review the final diff.
  Host-side `make dep` support is outside this feature.

## GitHub authentication

The target behavior is described in
[Planned: GitHub authentication](DESIGN.md#planned-github-authentication).
This feature is not implemented. Native dependency installation builds on the
Fedora/native-package work above; README support claims remain unchanged until
implementation.

- [ ] Add `github.enabled` (default false) and `github.token_command` (default
  empty argument vector) to configuration types, validation, cloning, merging,
  and deterministic output. Allow project overrides of `enabled`, but reject
  `token_command` in project files even when disabled or empty. Reject competing
  `GH_TOKEN`/`GITHUB_TOKEN` settings in `env` or `env_passthrough` when enabled.
  Preserve manual behavior when disabled and never resolve tokens for config
  inspection, lifecycle-only commands, or existing-VM no-op creation.
- [ ] Resolve nonempty host `GH_TOKEN`, then `GITHUB_TOKEN`, then the explicitly
  configured host command. Execute the argument vector without a shell from
  host home, with closed stdin, cancellation, and a 30-second timeout. Validate
  single-line tokens, allowing a trailing LF/CRLF from command output; reject
  malformed values rather than falling through. Report missing credentials
  and command failures without exposing captured output or command arguments.
  Resolve once per invocation, reuse through recreation, and do not cache
  credentials across invocations or automatically read host credential stores.
- [ ] Add `git` and `gh` as automatic dependencies through native package
  backends even with `packages: []`. Verify guest commands are available.
  Prepare GitHub integration after Git copying and before setup, including
  `copy_git_config: false`. Forward the selected token as `GH_TOKEN` to setup
  and shell/make/agent commands and their descendants, without adding it to
  package-manager commands or performing unconditional API probes.
- [ ] Build invocation-scoped runtime Git configuration, preserving valid
  existing `GIT_CONFIG_*` entries and rejecting malformed ones. Reset inherited
  GitHub HTTPS helpers and select `gh auth git-credential`. Rewrite
  `git@github.com:` and `ssh://git@github.com/` to HTTPS within the invocation;
  report conflicting copied Git rules that prevent HTTPS. Keep other hosts
  unaffected and shared repository remotes unchanged. Do not run persistent
  login/setup-git commands or store tokens in URLs, wrappers, or config files.
- [ ] Add table-driven configuration and resolver tests with `gotest.tools/v3`
  and larger fixtures in `testdata/`. Cover defaults, inheritance, global-only
  command validation, disabled mode, token precedence, missing/malformed tokens,
  command arguments without shell expansion, host-home working directory,
  empty/multiline output, failure, timeout, cancellation, and diagnostics that
  omit secrets from stdout and stderr. Verify read-only/no-op workflows do not
  execute token commands and configuration output never resolves tokens.
- [ ] Test guest dependency installation, setup ordering, nested-agent
  inheritance, concurrent invocations with different tokens, recreation, and
  re-resolution on the next invocation. Exercise HTTPS fetch/push and both
  supported SSH URL forms using isolated Git fixtures; cover conflicting host
  credential helpers/URL rewrites and pre-existing runtime Git settings.
  Assert no persistent login, credential files, token-bearing wrappers, shared
  remote edits, or changes to unrelated Git hosts.
- [ ] Validate disposable Ubuntu and Fedora VMs with a dedicated repository
  and token. Exercise private fetch/push, GitHub API and PR operations, setup
  and agent subprocesses, SSH-to-HTTPS rewriting, disabled support, and
  concurrent sessions. Verify insufficient permissions and expired tokens
  produce useful failures. Keep token values out of recorded validation output.
- [ ] At implementation time, update README with environment and global token
  command examples, required repository permissions, and token-lifetime and
  process-argument visibility limits. Promote the planned DESIGN.md section to
  current behavior. Enable prek and run `make lint`, `make test`, and
  `make build`. Enterprise hosts, SSH-agent forwarding, token minting, and
  automatic renewal remain outside this feature.

## YAML development configuration

The target behavior is described in the current development configuration
section of DESIGN.md. The YAML configuration rollout is complete.

- [x] Add and pin `go.yaml.in/yaml/v3`. Replace JSON decoding with YAML node
  validation and retain setting presence, defaults, merging, inheritance,
  package validation, and environment restrictions. Reject unknown/duplicate
  keys, nulls, invalid types, invalid UTF-8, and multiple documents; include
  the source filename and available location information in errors.
- [x] Switch global/project filename constants, including the exported project
  filename, to `config.yaml` and `.lja.yaml`. Update project discovery to use
  the new marker. Remove legacy JSON filename support without adding migration
  tooling or fallback; leave Lima JSON handling unchanged.
- [x] Replace the JSON-specific output representation, `AsJSON`, and CLI
  encoding helper with YAML equivalents. Emit deterministic effective settings
  with two-space indentation, empty collections, and a trailing newline,
  without resolving passthrough values or emitting setup provenance.
- [x] Convert configuration fixtures and add table-driven tests using
  `gotest.tools/v3`, with larger examples and expected output in `testdata/`.
  Cover missing files, empty mappings, omitted versus empty settings, global
  and project precedence, inheritance switches, comments, multiline commands,
  quoted numeric/boolean environment strings, rejected scalar coercions,
  unknown/duplicate keys, nulls, invalid UTF-8, and multiple documents.
- [x] Cover `.lja.yaml` discovery from descendants, explicit project selection,
  legacy JSON filenames no longer acting as markers or configuration sources,
  deterministic YAML output, and unchanged environment resolution behavior.

## Codex TOML editing library

The target behavior is described in the current Codex TOML editing section of
DESIGN.md.

- [x] Verify `github.com/pelletier/go-toml/v2/unstable/edit` at pinned version
  `v2.4.4-0.20260718201843-686c980c4758` against golden
  preservation fixtures and pin the tested version. Exercise semantic lookup
  and insertion for ordinary tables, dotted keys, and inline tables, including
  directories containing dots, quotes, backslashes, and Unicode. If the API
  cannot meet the design, record the specific gap here and defer replacement
  instead of accepting whole-document reformatting.
- [x] Replace the hand-written parser with library edits behind the existing
  `CodexTrustedConfig` signature. Preserve canonicalization, deduplication,
  accepted trust values, insertion of missing entries, and no-op detection.
  Validate input and output; report malformed TOML, duplicate definitions,
  incompatible target types, and edit failures before any write.
- [x] Add table-driven golden tests for replacing and inserting trust settings,
  creating project entries, preserving comments and unusual spacing, unrelated
  multiline strings/arrays and nested tables, LF/CRLF, missing final newline,
  and byte-identical repeated updates. Assert that unrelated content stays
  unchanged and the resulting semantic change affects only requested trust.
- [x] Retain and extend persistence checks for locking, atomic replacement,
  preserved file modes, `0600` new files, rejected symlinks and unsafe paths,
  unchanged files on errors, and no writes for already trusted directories.
  Keep login-only launches from changing trust.

## Complete each implementation

- [x] Update README.md examples and behavior descriptions alongside the code
  and tests. Promote the corresponding planned DESIGN.md section to current
  behavior, removing superseded descriptions and completed backlog items.
- [x] Ensure the prek hook is installed and run `make lint`, `make test`, and
  `make build` after implementation. Review the diff for unintended formatting
  changes and keep dependencies scoped to the implemented feature.

## GPG real-Lima validation

The implementation and isolated tests are complete; the remaining validation
requires a session with access to Lima's host state. The current nono sandbox
does not grant access to `~/.lima`.

- [ ] In a disposable Lima VM and disposable host `GNUPGHOME`, enable
  `gpg_forwarding`. Run `lja shell -- gpg --list-keys`, create a detached
  signature in the guest, and verify it on the host. Encrypt on the host and
  decrypt in the guest. Confirm no private-key files were copied.
- [ ] Exercise host GUI pinentry with a passphrase-protected key, including
  cancellation and retry, and test the supported host terminal-pinentry setup.
- [ ] Run simultaneous LJA shells; exiting one must leave the other usable.
  Confirm setup and the selected command share the temporary home. Verify
  nested agents inherit it, and background jobs lose access after LJA exits.
- [ ] Interrupt and SIGKILL LJA with an active GPG connection. Confirm the host
  agent is unreachable through any surviving guest socket, then remove any
  orphan SSH processes and inert temporary directories from the killed run.
- [ ] Recreate with forwarding enabled. Verify candidate forwarding closes
  before stop/rename, setup runs once, and the selected command uses a fresh
  final-VM session. Exercise setup failure and restoration paths.
- [ ] Disable forwarding for the next invocation and verify no host GPG process
  or forwarding is started. Confirm existing-VM no-op create and read-only
  lifecycle commands work without host GPG installed.
