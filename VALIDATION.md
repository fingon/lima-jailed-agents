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

## GitHub authentication

GitHub validation was run on 2026-09-24 with the same Lima 2.2.0 VZ setup.
Ubuntu 26.04 installed `gh` 2.46.0 and Fedora 44 installed `gh` 2.97.0
through their native package backends. A temporary private repository was
used for the checks; its pull request, branch, and contents were removed
afterward. The repository itself could not be deleted because the validation
token did not have the `delete_repo` scope.

Both guests passed setup with a host `gh auth token` command, received
`GH_TOKEN`, and successfully ran a GitHub API query from a setup subprocess.
Ubuntu fetched and pushed through a `git@github.com:` remote, then created a
pull request with `gh`; Fedora fetched through an
`ssh://git@github.com/` remote. Both remotes were observed as HTTPS inside
the invocation. A real Codex subprocess launched successfully in each guest
and reported Codex CLI 0.156.1. Two concurrent Ubuntu shell invocations both
completed API, token-inheritance, and URL-rewrite checks.

Disabling GitHub support on the Fedora project left both `GH_TOKEN` and
`GITHUB_TOKEN` unset. A deliberately invalid token produced an HTTP 401
`Bad credentials` failure without exposing the token. The authenticated token
also produced GitHub's explicit `admin:public_key` scope guidance when used
against an endpoint outside its permissions. No token values were included
in captured or recorded validation output.

## GPG real-Lima validation

On 2026-09-24, Ubuntu 26.04 under Lima 2.2.0 was validated with GnuPG
2.5.22 and a disposable host `GNUPGHOME`. The project enabled
`gpg_forwarding` and used `mountType: reverse-sshfs` because the validation
host could not obtain the default VZ virtiofs sandbox extension.

The guest listed the forwarded public key, created a detached signature that
the host verified, and decrypted ciphertext created by the host. The guest
session had no `private-keys-v1.d` directory before or after decryption, and
the disposable host keyring was destroyed after the run.

Concurrent GPG sessions were also run in the same Ubuntu VM. Each session's
setup marker was visible to its selected command, the temporary homes and
sockets were distinct, and the second session successfully listed keys after
the first session exited. A real Codex subprocess launched with forwarding
active. A background guest job started during a session did not retain usable
forwarding after LJA exited.

Interrupt cleanup was validated with active GPG connections. SIGINT removed the
guest temporary home and made its forwarded socket unavailable. After SIGKILL,
a raw Lima probe reported no running guest agent for the killed session; the
host had no matching GPG-forwarding SSH tunnel or `lja-gpg-*` proxy directory
left behind.
