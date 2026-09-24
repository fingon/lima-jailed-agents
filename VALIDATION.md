# Live validation

Validation was run on 2026-09-24 with Lima 2.2.0 on an Apple Silicon macOS
host. The disposable runs used Lima's VZ driver and these guest versions:

| Guest | Git | Make | Node | npm |
| --- | --- | --- | --- | --- |
| Ubuntu 26.04 | 2.53.0 | 4.4.1 | 22.22.1 | 9.2.0 |
| Fedora 44 | 2.55.0 | 4.4.1 | 22.23.1 | 10.9.8 |

Both projects configured `git`, `make`, and the `codex`, `claude`, and
`opencode` agents. The agent versions installed and updated in both guests
were Codex CLI 0.156.1, Claude Code 2.1.281, and OpenCode 1.18.32.

The following scenarios passed for both guests:

- creation, native dependency installation, setup, shell, and make dispatch;
- repeat preparation and each configured agent's update command;
- GPG forwarding, including a forwarded `gpg --list-keys --with-colons`
  invocation and LJA's `GPG forwarding ready` probe;
- recreation with a replacement VM, preparation of the candidate, and
  restoration of the managed VM name.

Fedora's unversioned `/usr/bin/node` and `/usr/bin/npm` were runnable and
were provided by `nodejs22-bin` and `nodejs22-npm-bin`, respectively.

The default Lima state directory was inaccessible in the validation sandbox,
so the run used a temporary `LIMA_HOME`. Lima's default virtiofs mount also
failed to obtain a VZ Fuse sandbox extension in that environment; the
disposable project configurations therefore selected `mountType:
reverse-sshfs`. This is a validation-environment workaround, not a project
default. All disposable instances and temporary project/state directories
were removed after validation.
