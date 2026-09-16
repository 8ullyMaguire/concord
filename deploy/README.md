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
| `CONCORD_FORGEJO_SECRET` | — | Webhook HMAC secret |

## Ports

- **8006**: Concord API (Cloudflare route)
- **5432**: PostgreSQL (gravity)
- **8191**: Ollama (local)
