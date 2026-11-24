# 04 - CLI contract and UI rules

## CLI contract (do not rename commands without asking)
The CLI controls the running daemon via the Unix socket `/run/slb/slb.sock` (mode `0660`), never by editing process state directly. The config file is the source of truth for persistence.

```text
slb init                      # generate /etc/slb/config.json interactively
slb run                       # daemon entrypoint (systemd)
slb status [--json]           # VIP owner, services, backends, connection counts
slb health                    # local health for keepalived (exit 0/1, <1s)
slb config show|validate|diff|reload|push
slb backend add|remove|enable|disable|drain <addr>
slb ha init|status            # render keepalived.conf, show VRRP state
slb conns [--backend X]       # live connections, pins
```
- Commands that return data support `--json`; exit codes are documented and tested.
- `drain` stops new connections to a backend and lets existing ones finish, up to a configured maximum wait.
- `config push` validates locally, copies to the peer node over SSH, and triggers a reload there.

## UI rules (milestone 8+)
- Same control API as the CLI; feature parity required. Embed assets with `embed`.
- Safe by default: bind to localhost or the management interface, authentication required, CSRF protection, audit log, no secrets in responses.
- Every change: validate, preview diff, apply, auto-rollback if health fails.
- Ask before adding any frontend framework.
