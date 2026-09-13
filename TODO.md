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

The target behavior is described in DESIGN.md under "Planned: library-based
Codex TOML editing". Keep the current editor until preservation is verified.

- [ ] Verify `github.com/pelletier/go-toml/v2/unstable/edit` against golden
  preservation fixtures and pin the tested version. Exercise semantic lookup
  and insertion for ordinary tables, dotted keys, and inline tables, including
  directories containing dots, quotes, backslashes, and Unicode. If the API
  cannot meet the design, record the specific gap here and defer replacement
  instead of accepting whole-document reformatting.
- [ ] Replace the hand-written parser with library edits behind the existing
  `CodexTrustedConfig` signature. Preserve canonicalization, deduplication,
  accepted trust values, insertion of missing entries, and no-op detection.
  Validate input and output; report malformed TOML, duplicate definitions,
  incompatible target types, and edit failures before any write.
- [ ] Add table-driven golden tests for replacing and inserting trust settings,
  creating project entries, preserving comments and unusual spacing, unrelated
  multiline strings/arrays and nested tables, LF/CRLF, missing final newline,
  and byte-identical repeated updates. Assert that unrelated content stays
  unchanged and the resulting semantic change affects only requested trust.
- [ ] Retain and extend persistence checks for locking, atomic replacement,
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
