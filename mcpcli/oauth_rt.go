package mcpcli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/oauth2"
)

// hasUsableAuth reports whether v looks like a complete Authorization header
// value (a scheme followed by a non-empty credential). After env-expansion,
// `Authorization: Bearer ${UNSET}` collapses to `"Bearer "`, which we treat
// as "no token" so the OAuth flow runs instead of bypassing on a value that
// would always 401 anyway.
func hasUsableAuth(v string) bool {
	return len(strings.Fields(v)) >= 2
}

// oauthRoundTripper sits between headerRoundTripper and the network. It
// reactively detects MCP servers that require OAuth (RFC 9728 challenge in
// `WWW-Authenticate`) and runs the full discovery → DCR → PKCE flow on
// demand. Tokens are persisted via the configured tokenStore.
//
// Layering: incoming request flows headerRoundTripper -> oauthRoundTripper
// -> base. headerRoundTripper may have already set `Authorization` from
// static config, in which case oauthRoundTripper stays out of the way.
type oauthRoundTripper struct {
	base      http.RoundTripper
	store     tokenStore
	server    string // logical server name from ServerSpec.Name
	serverURL string // for env-passing to execStore commands
	clientID  string // pre-registered client_id, optional
	cbPort    int    // 0 → ephemeral
	stderr    io.Writer

	mu sync.Mutex
}

func (o *oauthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Static Authorization wins, but only when it carries an actual credential.
	// `Bearer ` (env-unset) falls through to the OAuth flow.
	if hasUsableAuth(req.Header.Get("Authorization")) {
		return o.base.RoundTrip(req)
	}
	ctx := req.Context()

	// Try to attach a cached token (refreshing if stale) before the first hop.
	if tok, err := o.attachCachedToken(ctx, req); err == nil && tok != nil {
		// Authorization was set; fall through to send.
	} else if err != nil {
		fmt.Fprintf(o.stderr, "warning: could not load cached oauth token for %s: %v\n", o.server, err)
	}

	resp, err := o.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	prmURL := parseResourceMetadataURL(resp.Header.Get("WWW-Authenticate"))
	if prmURL == "" {
		return resp, nil
	}
	// Drain body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Don't retry if we can't replay the request body.
	if req.Body != nil && req.GetBody == nil {
		// Run flow anyway so the next call has a token, but surface the 401 now.
		if err := o.acquireAndStore(ctx, prmURL); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("mcp request to %s required OAuth and request body cannot be replayed; retry the command", req.URL)
	}

	if err := o.acquireAndStore(ctx, prmURL); err != nil {
		return nil, err
	}
	if err := o.attachToken(ctx, req); err != nil {
		return nil, err
	}
	if req.Body != nil && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("rewind body for oauth retry: %w", err)
		}
		req.Body = body
	}
	return o.base.RoundTrip(req)
}

// attachCachedToken loads the token from the store, refreshes it via the
// stored token endpoint if needed, and sets the Authorization header. Returns
// (nil, nil) if no token is cached yet.
func (o *oauthRoundTripper) attachCachedToken(ctx context.Context, req *http.Request) (*oauth2.Token, error) {
	pt, err := o.store.Get(ctx, o.server)
	if err != nil || pt == nil || pt.Token == nil {
		return nil, err
	}
	tok := pt.Token
	if !tok.Valid() && tok.RefreshToken != "" && pt.TokenEndpoint != "" {
		cfg := &oauth2.Config{
			ClientID: pt.ClientID,
			Endpoint: oauth2.Endpoint{TokenURL: pt.TokenEndpoint},
			Scopes:   pt.Scopes,
		}
		newTok, err := cfg.TokenSource(ctx, tok).Token()
		if err != nil {
			// Treat refresh failure as "no usable token"; reactive 401 will
			// run a fresh interactive flow.
			return nil, nil
		}
		if newTok.AccessToken != tok.AccessToken {
			pt.Token = newTok
			if err := o.store.Set(ctx, o.server, o.serverURL, pt); err != nil {
				fmt.Fprintf(o.stderr, "warning: persist refreshed oauth token for %s: %v\n", o.server, err)
			}
			tok = newTok
		}
	}
	tok.SetAuthHeader(req)
	return tok, nil
}

// attachToken loads the cached token (no refresh) and sets Authorization.
// Used after an interactive flow stored a fresh token.
func (o *oauthRoundTripper) attachToken(ctx context.Context, req *http.Request) error {
	pt, err := o.store.Get(ctx, o.server)
	if err != nil {
		return fmt.Errorf("read oauth token after flow: %w", err)
	}
	if pt == nil || pt.Token == nil {
		return fmt.Errorf("oauth token missing after flow for %s", o.server)
	}
	pt.Token.SetAuthHeader(req)
	return nil
}

// acquireAndStore runs the interactive OAuth flow under the round tripper's
// mutex so only one browser opens even when many requests race on 401.
func (o *oauthRoundTripper) acquireAndStore(ctx context.Context, prmURL string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Double-check: another goroutine may have completed a flow while we
	// were waiting on the lock.
	if pt, err := o.store.Get(ctx, o.server); err == nil && pt != nil && pt.Token != nil && pt.Token.Valid() {
		return nil
	}
	pt, err := performInteractiveFlow(ctx, oauthFlowParams{
		PRMURL:       prmURL,
		CallbackPort: o.cbPort,
		ClientID:     o.clientID,
		Stderr:       o.stderr,
	})
	if err != nil {
		return err
	}
	if err := o.store.Set(ctx, o.server, o.serverURL, pt); err != nil {
		return fmt.Errorf("persist oauth token: %w", err)
	}
	return nil
}
