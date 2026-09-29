package config

import (
	"os"
	"path/filepath"
	"strconv"
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

// DefaultTrustLevelMin is the highest level trust_config.max_level allows, so
// the default gate is "only the most trusted account" — the safe reading of
// "configurable, with highest tl by default".
const DefaultTrustLevelMin = 1

// TrustLevelMin returns the minimum trust level required to set a feature's
// status directly, from $CONCORD_TRUST_LEVEL_MIN.
//
// The name is deliberately explicit about what it gates: it is the threshold
// for the direct status route, not a general privilege level. Merge-request
// approval keeps its existing path and is not affected, so nothing that
// worked before this gate existed changes behaviour.
//
// An unparseable or negative value falls back to the default rather than
// silently opening the gate — a typo in a systemd unit should not be the
// difference between a locked and an unlocked instance.
func TrustLevelMin() int {
	v := os.Getenv("CONCORD_TRUST_LEVEL_MIN")
	if v == "" {
		return DefaultTrustLevelMin
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return DefaultTrustLevelMin
	}
	return n
}
