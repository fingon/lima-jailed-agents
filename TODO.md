# Implementation backlog

The Go CLI, project identity and discovery, exact Lima lifecycle, package and
agent provisioning, state isolation, instruction synchronization, Codex trust
editing, Git copying, process-boundary tests, and documentation are
implemented. The remaining work is integration validation against real Lima
instances.

## Real-Lima shared-state validation

- Create two disposable projects using one dedicated shared state root. Verify
  the exact writable project/state mounts and absence of unrelated mounts.
- Authenticate Codex, Claude Code, and OpenCode explicitly. Verify that a
  second VM reuses settings and credentials, including Claude auxiliary state
  and OpenCode XDG data.
- Run agents concurrently in two VMs and inspect database locking, concurrent
  writes, session history, and token refresh. Do not claim concurrent access
  is supported until agent-specific behavior is understood.
- Replace a disposable VM and verify that selected settings, credentials, and
  session history survive. Exercise project-local and shared modes, including
  additional-agent wrappers.
- Copy one stopped project's local state into an empty shared root and verify
  that the old VM is rejected in shared mode, remains usable with
  `--project-state`, and can be replaced deliberately.

## Real-Lima development validation

- Configure global and project setup scripts. Verify ordering, repeated-launch
  retry behavior, shell/update coverage, and lock behavior.
- Select `ninja-build` without Make in a fresh VM; also test an empty package
  list and `copy_git_config: false`.
- Pass disposable caller variables containing spaces, quotes, newlines, and
  empty values through setup, shells, agents, and child agents. Verify that
  unlisted variables and caller values do not appear in wrappers or `lja
  config`.
- Verify host Git configuration, recursive includes, excludes, and refresh
  behavior in a disposable VM.
