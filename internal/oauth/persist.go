// Package oauth provides helpers for managing OAuth2 tokens across restarts.
package oauth

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"golang.org/x/oauth2"
)

// NewPersistingTokenSource wraps base so that whenever the OAuth server issues
// a new refresh token (token rotation), persist is called with the new value.
// Pass the current refresh token as initialRefresh so the first call can detect
// a change.
func NewPersistingTokenSource(base oauth2.TokenSource, initialRefresh string, persist func(string)) oauth2.TokenSource {
	return &persistingTokenSource{
		base:    base,
		current: initialRefresh,
		persist: persist,
	}
}

type persistingTokenSource struct {
	base    oauth2.TokenSource
	persist func(string)
	current string
	mu      sync.Mutex
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	t, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		return t, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if t.RefreshToken != p.current {
		p.current = t.RefreshToken
		if p.persist != nil {
			p.persist(t.RefreshToken)
		}
	}
	return t, nil
}

// EnvFilePersist returns a persist callback that rewrites envKey in the .env
// file at envFile whenever the OAuth server issues a new refresh token.
// Use this as the persist argument to NewPersistingTokenSource.
func EnvFilePersist(envFile, envKey string) func(string) {
	return func(newToken string) {
		if err := updateDotEnv(envFile, envKey, newToken); err != nil {
			slog.Warn("oauth: failed to persist rotated refresh token — restart will require re-auth",
				"key", envKey, "error", err)
		} else {
			slog.Info("oauth: refresh token rotated and persisted", "key", envKey)
		}
	}
}

// updateDotEnv replaces the value of key in the .env file at path.
// If the key is not present, it is appended. The file is rewritten atomically
// via a temp file so a crash mid-write cannot corrupt it.
func updateDotEnv(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	prefix := key + "="
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			lines[i] = prefix + value
			replaced = true
			break
		}
	}
	if !replaced {
		// Remove trailing blank line so we don't double-space the file.
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines[len(lines)-1] = prefix + value
		} else {
			lines = append(lines, prefix+value)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	return os.Rename(tmp, path)
}
