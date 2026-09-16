package config

import (
	"os"
	"path/filepath"
)

// Config is Concord's runtime configuration. Flags override env vars;
// env vars override defaults.
type Config struct {
	DBPath        string // $CONCORD_DB
	Listen        string // $CONCORD_LISTEN
	WebhookSecret string // $CONCORD_FORGEJO_SECRET (optional, HMAC for /api/v1/hooks/*)
}

func Load() Config {
	return Config{
		DBPath:        envOr("CONCORD_DB", defaultDBPath()),
		Listen:        envOr("CONCORD_LISTEN", "127.0.0.1:8410"),
		WebhookSecret: os.Getenv("CONCORD_FORGEJO_SECRET"),
	}
}

// defaultDBPath keeps live SQLite on local disk per the user's storage
// policy: $XDG_DATA_HOME/concord/concord.db (default ~/.local/share/...).
func defaultDBPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "concord.db"
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "concord", "concord.db")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
