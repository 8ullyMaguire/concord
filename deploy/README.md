# Concord deployment on thinkcentre

## Systemd service

```bash
# Enable and start
systemctl --user enable concord.service
systemctl --user start concord.service

# Status
systemctl --user status concord.service

# Logs
journalctl --user -u concord.service -f
```

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `CONCORD_LISTEN` | `127.0.0.1:8410` | Listen address (set to `0.0.0.0:8006` for Cloudflare) |
| `CONCORD_DB` | `~/.local/share/concord/concord.db` | SQLite database path |
| `CONCORD_FORGEJO_SECRET` | — | Webhook HMAC secret. **Read from `~/.config/concord/forge-webhook.env`** (0600) via the unit's `EnvironmentFile=`, never from this table. Unset means `handleForgejoWebhook` fails closed and `forge_events` stays empty |

### The webhook, end to end

`forge_events` is written by four arms of `POST /api/v1/hooks/forgejo` (push, `pull_request`,
`pull_request_review`, `pull_request_comment`), and it is the only source for R12's newcomer
responsiveness. Nothing in the tree can create the forge side of that path, so a shipped-and-green
metric can sit at zero rows indefinitely — measured 2026-11-12: zero hooks on every repository AND
no secret in the unit, so no event could ever have arrived.

```bash
# 1. the secret exists on the host (this is the only copy; the forge stores its own)
ssh thinkcentre 'ls -l ~/.config/concord/forge-webhook.env'
# 2. one hook per repository, with the same secret
TOKEN=$(python3 -c "import json;print(json.load(open('$HOME/.local/share/forgejo-cli/keys.json'))['hosts']['git.polarisocial.xyz']['token'])")
curl -s -H "Authorization: token $TOKEN" \
  "https://git.polarisocial.xyz/api/v1/repos/hirrolot19/concord/hooks"
# 3. a test delivery, then the row it should have written
curl -s -X POST -H "Authorization: token $TOKEN" \
  "https://git.polarisocial.xyz/api/v1/repos/hirrolot19/concord/hooks/<id>/tests"
ssh thinkcentre 'sqlite3 -readonly ~/.local/share/concord/concord.db "select count(*) from forge_events"'
```

## Ports

- **8006**: Concord API (Cloudflare route)
- **5432**: PostgreSQL (gravity)
- **8191**: Ollama (local)

## Timers (admin jobs)

`make deploy-admin` installs the three one-shot jobs and their units.

| Timer | When | Enabled by deploy-admin? | What it does |
|---|---|---|---|
| `concord-healthsync.timer` | hourly | no (enabled on this host) | reads repository health from GitHub / Forgejo / Codeberg |
| `concord-statsnapshot.timer` | 03:10 UTC daily | no | writes one `instance_stats` row for the day |
| `concord-proposalsweep.timer` | 03:20 UTC daily | **yes** | marks undecided proposals `stale` after `CONCORD_PROPOSAL_TTL_DAYS` (default 30, `0` disables) |

`deploy-admin` no longer runs `systemctl --user disable --now` on these: installing a unit is not a
decision about whether the job runs, and disabling on every install silently switched off an hourly
pass somebody had turned on. Enable what you want on the host and it survives the next deploy.

The two forge-touching jobs are installed disabled: switching them on spends someone's rate limit
against shared infrastructure, so whoever accepts that cost enables them. The sweep reads and
writes only the local database, and a sweep installed but never enabled is a queue that never
drains — so it is the one this target turns on.

Run it by hand before trusting the timer:

```bash
~/concord-deploy/proposalsweep -dry-run      # the list, no writes
~/concord-deploy/proposalsweep -v            # the list, and the marks
journalctl --user -u concord-proposalsweep.service -n 20
```

