// Package config resolves where things live.
package config

import (
	"os"
	"path/filepath"
)

// EnvDB overrides the database location for every command at once.
const EnvDB = "AGENT_EVAL_DB"

// DefaultDBPath returns the database path used when -db is not given.
//
// A fixed, absolute location rather than a file in the working directory. The
// collector runs from wherever it was started — a systemd unit, a container, a
// stray terminal — and the reader runs from wherever you happen to be standing.
// A relative default means those two disagree and `agent-eval traces` reports an
// empty database that is simply a different file. Being wrong that way is
// silent, which is the worst kind.
//
// Resolution order: AGENT_EVAL_DB, then XDG_DATA_HOME, then ~/.local/share.
// Falls back to the working directory only when there is no home to speak of,
// which in practice means a container without one.
func DefaultDBPath() string {
	if p := os.Getenv(EnvDB); p != "" {
		return p
	}

	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "agent-eval.db"
		}
		dir = filepath.Join(home, ".local", "share")
	}

	return filepath.Join(dir, "agent-eval", "agent-eval.db")
}

// EnsureParent creates the directory holding path. Called before opening the
// database so a first run does not fail on a directory nobody created.
func EnsureParent(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
