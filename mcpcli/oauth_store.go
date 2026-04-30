package mcpcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
)

// persistedToken is what we serialize per server. The oauth2.Token already
// JSON-marshals to the standard fields; we wrap it with the metadata needed
// to reconstruct an oauth2.Config for refresh without redoing discovery.
type persistedToken struct {
	Token         *oauth2.Token `json:"token"`
	TokenEndpoint string        `json:"token_endpoint"`
	AuthURL       string        `json:"auth_url,omitempty"`
	ClientID      string        `json:"client_id"`
	Scopes        []string      `json:"scopes,omitempty"`
}

// tokenStore is the persistence layer used by the OAuth round tripper.
// Get returns (nil, nil) if no token has been saved for the server yet.
type tokenStore interface {
	Get(ctx context.Context, server string) (*persistedToken, error)
	Set(ctx context.Context, server, serverURL string, t *persistedToken) error
}

// oauthStoreOptions configures store selection at runtime from CLI flags.
type oauthStoreOptions struct {
	CacheDir string // fileStore root; if empty, defaults to XDG cache.
	GetCmd   string // exec store: prints token JSON on stdout.
	SetCmd   string // exec store: reads token JSON from stdin.
}

// newTokenStore builds the configured store. Returns an error if only one of
// GetCmd/SetCmd is provided (both must be set together).
func newTokenStore(opts oauthStoreOptions) (tokenStore, error) {
	getSet := opts.GetCmd != "" || opts.SetCmd != ""
	if getSet && (opts.GetCmd == "" || opts.SetCmd == "") {
		return nil, errors.New("--oauth-token-get-cmd and --oauth-token-set-cmd must be used together")
	}
	if getSet {
		return &execStore{getCmd: opts.GetCmd, setCmd: opts.SetCmd}, nil
	}
	dir := opts.CacheDir
	if dir == "" {
		d, err := defaultCacheDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	return &fileStore{dir: dir}, nil
}

// defaultCacheDir resolves $XDG_CACHE_HOME/mcp-cli-gen/tokens, falling back
// to $HOME/.cache/mcp-cli-gen/tokens.
func defaultCacheDir() (string, error) {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "mcp-cli-gen", "tokens"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for token cache: %w", err)
	}
	return filepath.Join(home, ".cache", "mcp-cli-gen", "tokens"), nil
}

// sanitizeServerName produces a filesystem-safe slug for the per-server
// cache file. Anything outside [A-Za-z0-9._-] is replaced with '_'.
func sanitizeServerName(name string) string {
	if name == "" {
		return "_"
	}
	b := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}

// fileStore persists each server's token as a JSON file at <dir>/<server>.json.
type fileStore struct {
	dir string
}

func (f *fileStore) path(server string) string {
	return filepath.Join(f.dir, sanitizeServerName(server)+".json")
}

func (f *fileStore) Get(_ context.Context, server string) (*persistedToken, error) {
	b, err := os.ReadFile(f.path(server))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read oauth token: %w", err)
	}
	var pt persistedToken
	if err := json.Unmarshal(b, &pt); err != nil {
		return nil, fmt.Errorf("decode oauth token at %s: %w", f.path(server), err)
	}
	return &pt, nil
}

func (f *fileStore) Set(_ context.Context, server, _ string, t *persistedToken) error {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return fmt.Errorf("create token cache dir: %w", err)
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("encode oauth token: %w", err)
	}
	p := f.path(server)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return fmt.Errorf("write oauth token to %s: %w", p, err)
	}
	return nil
}

// execStore delegates persistence to user-supplied shell commands so that
// arbitrary backends (e.g. macOS `security`, 1Password `op`) can be wrapped.
type execStore struct {
	getCmd string
	setCmd string
}

func (e *execStore) env(server, serverURL string) []string {
	env := os.Environ()
	env = append(env, "MCPCLI_OAUTH_SERVER="+server)
	if serverURL != "" {
		env = append(env, "MCPCLI_OAUTH_URL="+serverURL)
	}
	return env
}

func (e *execStore) Get(ctx context.Context, server string) (*persistedToken, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", e.getCmd)
	cmd.Env = e.env(server, "")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Non-zero exit is treated as "no token cached yet" so wrappers
		// can simply return false on missing entries (e.g. `security` returns 44).
		return nil, nil
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return nil, nil
	}
	var pt persistedToken
	if err := json.Unmarshal(out, &pt); err != nil {
		return nil, fmt.Errorf("decode token from --oauth-token-get-cmd: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return &pt, nil
}

func (e *execStore) Set(ctx context.Context, server, serverURL string, t *persistedToken) error {
	b, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("encode oauth token: %w", err)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", e.setCmd)
	cmd.Env = e.env(server, serverURL)
	cmd.Stdin = bytes.NewReader(b)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("--oauth-token-set-cmd failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
