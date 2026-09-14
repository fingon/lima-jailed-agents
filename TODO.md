# Implementation backlog

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
