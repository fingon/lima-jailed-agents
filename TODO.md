# Implementation backlog

## Logical tools

- [ ] Implement the [planned logical tools setting](DESIGN.md#logical-tools-planned).
  Add `tools` parsing and validation, `DevelopmentConfig.Tools`, cloning, and
  deterministic configuration output. Default to an empty list; match package
  replacement, stable deduplication, and explicit-empty inheritance behavior.
  Initially accept only `uv` and `prek`.
- [ ] Add built-in tool definitions and transitive dependency resolution:
  native `pipx` → `uv` via `pipx install uv` → `prek` via
  `uv tool install prek`. Deduplicate prerequisites and install in dependency
  order, using the guest package backend for native dependencies even with an
  empty configured package list.
- [ ] Integrate tool preparation into the locked development-preparation flow
  after native packages and before setup. Install as the guest user, reuse
  working executables without upgrading, verify installed commands, and make
  them available through the guest PATH bootstrap. Distinguish missing commands
  from failing commands and connection errors; report failures with tool and
  VM context.
- [ ] Add table-driven configuration and provisioning tests covering defaults,
  empty lists, inheritance, cloning/output, duplicates, unknown names, each
  dependency chain, reversed selection order, shared prerequisites, empty
  `packages`, existing installations, installation/probe failures, and tool
  availability during setup and launched commands. Validate both Ubuntu/Debian
  and Fedora native dependency handling, including live guest smoke checks.
- [ ] When support ships, update DESIGN.md to describe implemented behavior,
  add a brief README.md configuration example, and run `make check`. Keep
  version pinning, a tool-upgrade interface, and host `make dep` changes outside
  this initial feature.
