package mcpcli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestParseResourceMetadataURL(t *testing.T) {
	cases := []struct {
		name      string
		challenge string
		want      string
	}{
		{
			name:      "single quoted param",
			challenge: `Bearer resource_metadata="https://example.com/.well-known/oauth-protected-resource"`,
			want:      "https://example.com/.well-known/oauth-protected-resource",
		},
		{
			name:      "with error param first",
			challenge: `Bearer error="invalid_token", resource_metadata="https://x/.well-known/oauth-protected-resource"`,
			want:      "https://x/.well-known/oauth-protected-resource",
		},
		{
			name:      "unquoted value",
			challenge: `Bearer resource_metadata=https://x/.well-known/oauth-protected-resource`,
			want:      "https://x/.well-known/oauth-protected-resource",
		},
		{
			name:      "case insensitive scheme",
			challenge: `bearer resource_metadata="https://x/y"`,
			want:      "https://x/y",
		},
		{
			name:      "missing param",
			challenge: `Bearer realm="api"`,
			want:      "",
		},
		{
			name:      "wrong scheme",
			challenge: `Basic realm="api", resource_metadata="https://x/y"`,
			want:      "",
		},
		{
			name:      "empty",
			challenge: "",
			want:      "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseResourceMetadataURL(c.challenge); got != c.want {
				t.Errorf("parseResourceMetadataURL(%q) = %q, want %q", c.challenge, got, c.want)
			}
		})
	}
}

func TestASMetadataCandidates(t *testing.T) {
	cases := []struct {
		name   string
		issuer string
		want   []string
	}{
		{
			name:   "no path",
			issuer: "https://idp.example.com",
			want: []string{
				"https://idp.example.com/.well-known/oauth-authorization-server",
				"https://idp.example.com/.well-known/openid-configuration",
				"https://idp.example.com/.well-known/oauth-authorization-server",
			},
		},
		{
			name:   "tenant path (RFC 8414 inserted form)",
			issuer: "https://idp.example.com/tenant1",
			want: []string{
				"https://idp.example.com/.well-known/oauth-authorization-server/tenant1",
				"https://idp.example.com/tenant1/.well-known/openid-configuration",
				"https://idp.example.com/tenant1/.well-known/oauth-authorization-server",
			},
		},
		{
			name:   "trailing slash stripped",
			issuer: "https://idp.example.com/tenant1/",
			want: []string{
				"https://idp.example.com/.well-known/oauth-authorization-server/tenant1",
				"https://idp.example.com/tenant1/.well-known/openid-configuration",
				"https://idp.example.com/tenant1/.well-known/oauth-authorization-server",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := asMetadataCandidates(c.issuer)
			if len(got) != len(c.want) {
				t.Fatalf("len mismatch: got %d, want %d (%v vs %v)", len(got), len(c.want), got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("candidate[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestHasUsableAuth(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"", false},
		{"Bearer", false},
		{"Bearer ", false},
		{"Bearer  \t  ", false},
		{"Bearer abc", true},
		{"Bearer  abc  ", true},
		{"Basic dXNlcjpwYXNz", true},
	}
	for _, c := range cases {
		if got := hasUsableAuth(c.v); got != c.want {
			t.Errorf("hasUsableAuth(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestSanitizeServerName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"github", "github"},
		{"My Server!", "My_Server_"},
		{"a/b\\c", "a_b_c"},
		{"", "_"},
	}
	for _, c := range cases {
		if got := sanitizeServerName(c.in); got != c.want {
			t.Errorf("sanitizeServerName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFileStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs := &fileStore{dir: filepath.Join(dir, "tokens")}

	if got, err := fs.Get(context.Background(), "missing"); err != nil || got != nil {
		t.Fatalf("Get on missing: got=%v err=%v", got, err)
	}

	pt := &persistedToken{
		Token: &oauth2.Token{
			AccessToken:  "atok",
			RefreshToken: "rtok",
			TokenType:    "Bearer",
			Expiry:       time.Now().Add(time.Hour).UTC(),
		},
		TokenEndpoint: "https://issuer/oauth/token",
		ClientID:      "client123",
		Scopes:        []string{"read", "write"},
	}
	if err := fs.Set(context.Background(), "github", "https://api/", pt); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := fs.Get(context.Background(), "github")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Token == nil {
		t.Fatalf("Get returned nil")
	}
	if got.Token.AccessToken != "atok" || got.Token.RefreshToken != "rtok" {
		t.Errorf("token mismatch: %+v", got.Token)
	}
	if got.TokenEndpoint != pt.TokenEndpoint || got.ClientID != pt.ClientID {
		t.Errorf("metadata mismatch: %+v", got)
	}

	// File mode should be 0600.
	info, err := os.Stat(fs.path("github"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file perm = %o, want 0600", info.Mode().Perm())
	}
}

func TestExecStore_RoundTrip(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	stash := filepath.Join(dir, "stash")
	getCmd := `if [ -f "` + stash + `" ]; then cat "` + stash + `"; else exit 1; fi`
	setCmd := `cat > "` + stash + `"`

	st := &execStore{getCmd: getCmd, setCmd: setCmd}

	if got, err := st.Get(context.Background(), "github"); err != nil || got != nil {
		t.Fatalf("Get on missing should be (nil,nil); got=%v err=%v", got, err)
	}

	pt := &persistedToken{
		Token:         &oauth2.Token{AccessToken: "atok", TokenType: "Bearer"},
		TokenEndpoint: "https://issuer/token",
		ClientID:      "cid",
	}
	if err := st.Set(context.Background(), "github", "https://api/", pt); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := st.Get(context.Background(), "github")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Token == nil || got.Token.AccessToken != "atok" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestNewTokenStore_BothOrNeither(t *testing.T) {
	if _, err := newTokenStore(oauthStoreOptions{GetCmd: "true"}); err == nil {
		t.Error("expected error when only GetCmd set")
	}
	if _, err := newTokenStore(oauthStoreOptions{SetCmd: "true"}); err == nil {
		t.Error("expected error when only SetCmd set")
	}
	if _, err := newTokenStore(oauthStoreOptions{GetCmd: "true", SetCmd: "true"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := newTokenStore(oauthStoreOptions{CacheDir: t.TempDir()}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// fakeOAuthServer wires up PRM, AS metadata, authorization, and token
// endpoints in a single httptest.Server so we can exercise the full reactive
// flow end-to-end.
type fakeOAuthServer struct {
	*httptest.Server
	mu      sync.Mutex
	issued  string // last access_token issued
	dcrSeen bool
}

func newFakeOAuthServer(t *testing.T) *fakeOAuthServer {
	t.Helper()
	f := &fakeOAuthServer{}
	mux := http.NewServeMux()

	// PRM (RFC 9728)
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              f.URL,
			"authorization_servers": []string{f.URL},
		})
	})
	// AS metadata (RFC 8414)
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           f.URL,
			"authorization_endpoint":           f.URL + "/authorize",
			"token_endpoint":                   f.URL + "/token",
			"registration_endpoint":            f.URL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	// DCR (RFC 7591)
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.dcrSeen = true
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dcr-client-id"})
	})
	// Authorization endpoint: immediately redirect with a code.
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := q.Get("state")
		redirect := q.Get("redirect_uri")
		u, _ := url.Parse(redirect)
		rq := u.Query()
		rq.Set("code", "auth-code-1")
		rq.Set("state", state)
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	// Token endpoint
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code_verifier") == "" {
				http.Error(w, "missing PKCE verifier", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.issued = "access-token-1"
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token-1",
				"token_type":    "Bearer",
				"refresh_token": "refresh-1",
				"expires_in":    3600,
			})
		default:
			http.Error(w, "unsupported grant", http.StatusBadRequest)
		}
	})
	f.Server = httptest.NewServer(mux)
	return f
}

func (f *fakeOAuthServer) lastIssued() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.issued
}

func TestOAuthRoundTripper_ReactiveFlow(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh available for execStore comparison")
	}
	auth := newFakeOAuthServer(t)
	defer auth.Close()

	// The "MCP" server: returns 401 with WWW-Authenticate until a valid
	// Bearer is presented, then echoes ok.
	var seenBearer string
	var mu sync.Mutex
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got != "Bearer access-token-1" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+auth.URL+`/.well-known/oauth-protected-resource"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		seenBearer = got
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	defer mcp.Close()

	// Replace browser-open with an HTTP GET that follows the redirect into
	// the local callback server; that drives the auth handshake without a
	// real browser.
	prev := openBrowser
	openBrowser = func(target string) error {
		go func() {
			c := &http.Client{Timeout: 5 * time.Second}
			resp, err := c.Get(target)
			if err == nil && resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	defer func() { openBrowser = prev }()

	store := &fileStore{dir: filepath.Join(t.TempDir(), "tokens")}
	rt := &oauthRoundTripper{
		base:      http.DefaultTransport,
		store:     store,
		server:    "test",
		serverURL: mcp.URL,
		stderr:    io.Discard,
	}
	client := &http.Client{Transport: rt, Timeout: 30 * time.Second}

	resp, err := client.Get(mcp.URL + "/")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("expected 200 ok after flow; got status=%d body=%q", resp.StatusCode, string(body))
	}
	if !auth.dcrSeen {
		t.Errorf("expected DCR registration call")
	}
	if got := auth.lastIssued(); got != "access-token-1" {
		t.Errorf("expected access-token-1 issued, got %q", got)
	}
	mu.Lock()
	bearerCopy := seenBearer
	mu.Unlock()
	if !strings.Contains(bearerCopy, "access-token-1") {
		t.Errorf("MCP did not see Bearer token: %q", bearerCopy)
	}

	// Token must be persisted on disk so the next process can reuse it.
	pt, err := store.Get(context.Background(), "test")
	if err != nil || pt == nil || pt.Token == nil || pt.Token.AccessToken != "access-token-1" {
		t.Errorf("persisted token wrong: pt=%+v err=%v", pt, err)
	}

	// Second request reuses the cached token (no new DCR, no new exchange).
	auth.mu.Lock()
	auth.dcrSeen = false
	auth.issued = ""
	auth.mu.Unlock()
	resp2, err := client.Get(mcp.URL + "/")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second request not OK: %d", resp2.StatusCode)
	}
	if auth.dcrSeen {
		t.Errorf("did not expect DCR on second request")
	}
	if auth.lastIssued() != "" {
		t.Errorf("did not expect new token issuance on second request, got %q", auth.lastIssued())
	}
}

func TestOAuthRoundTripper_StaticAuthorizationWins(t *testing.T) {
	called := 0
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Header.Get("Authorization") != "Bearer static-tok" {
			t.Errorf("expected Bearer static-tok, got %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer mcp.Close()

	rt := &oauthRoundTripper{
		base:   http.DefaultTransport,
		store:  &fileStore{dir: filepath.Join(t.TempDir(), "tokens")},
		server: "test",
		stderr: io.Discard,
	}
	req, _ := http.NewRequest(http.MethodGet, mcp.URL, nil)
	req.Header.Set("Authorization", "Bearer static-tok")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if called != 1 {
		t.Errorf("expected exactly one upstream hit, got %d", called)
	}
}

func TestOAuthRoundTripper_EmptyBearerTriggersFlow(t *testing.T) {
	auth := newFakeOAuthServer(t)
	defer auth.Close()

	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token-1" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+auth.URL+`/.well-known/oauth-protected-resource"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer mcp.Close()

	prev := openBrowser
	openBrowser = func(target string) error {
		go func() {
			c := &http.Client{Timeout: 5 * time.Second}
			resp, err := c.Get(target)
			if err == nil && resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	defer func() { openBrowser = prev }()

	rt := &oauthRoundTripper{
		base:      http.DefaultTransport,
		store:     &fileStore{dir: filepath.Join(t.TempDir(), "tokens")},
		server:    "test",
		serverURL: mcp.URL,
		stderr:    io.Discard,
	}
	client := &http.Client{Transport: rt, Timeout: 30 * time.Second}

	req, _ := http.NewRequest(http.MethodGet, mcp.URL+"/", nil)
	// Mimic header expansion of `Bearer ${UNSET}` — value is non-empty but
	// carries no credential. Must trigger the OAuth flow rather than passing
	// through and 401-ing.
	req.Header.Set("Authorization", "Bearer ")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("flow not triggered: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestResolveAS(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// AS metadata at the resource host root only (no PRM, no resource path).
	asDoc := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
		})
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server", asDoc)

	// PRM URL we'll advertise; deliberately returns 404 to exercise the
	// "PRM advertised but unreachable → fallback" branch.
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	resource, _ := url.Parse(srv.URL + "/v1/mcp")

	t.Run("no PRM advertised, falls through to origin form", func(t *testing.T) {
		got, err := resolveAS(context.Background(), resource, `Bearer realm="OAuth"`, io.Discard)
		if err != nil {
			t.Fatalf("resolveAS: %v", err)
		}
		if got.AuthorizationEndpoint != srv.URL+"/authorize" {
			t.Errorf("auth endpoint = %q, want %q", got.AuthorizationEndpoint, srv.URL+"/authorize")
		}
	})

	t.Run("PRM advertised but unreachable, warns and falls back", func(t *testing.T) {
		var stderr strings.Builder
		challenge := `Bearer resource_metadata="` + srv.URL + `/.well-known/oauth-protected-resource"`
		got, err := resolveAS(context.Background(), resource, challenge, &stderr)
		if err != nil {
			t.Fatalf("resolveAS: %v", err)
		}
		if got.TokenEndpoint != srv.URL+"/token" {
			t.Errorf("token endpoint = %q, want %q", got.TokenEndpoint, srv.URL+"/token")
		}
		if !strings.Contains(stderr.String(), "PRM-based AS discovery") {
			t.Errorf("expected PRM fallback warning, got %q", stderr.String())
		}
	})

	t.Run("no AS anywhere, returns error", func(t *testing.T) {
		// Point at a port that will never answer success: a different mux
		// with no AS endpoint.
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		}))
		defer dead.Close()
		deadResource, _ := url.Parse(dead.URL + "/v1/mcp")
		if _, err := resolveAS(context.Background(), deadResource, `Bearer`, io.Discard); err == nil {
			t.Fatal("expected error when no AS metadata is reachable")
		}
	})
}

func TestIsBearerChallenge(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"", false},
		{"Bearer", true},
		{"Bearer ", true},
		{`Bearer realm="api"`, true},
		{"bearer abc", true},
		{`Bearer realm="OAuth", error="invalid_token"`, true},
		{"Basic realm=api", false},
		{"DPoP", false},
		{"BearerToken", false}, // not separated by whitespace
	}
	for _, c := range cases {
		if got := isBearerChallenge(c.v); got != c.want {
			t.Errorf("isBearerChallenge(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}

// TestOAuthRoundTripper_FallbackASDirect covers servers (e.g. Atlassian Remote
// MCP) that issue a Bearer challenge without RFC 9728 resource_metadata and
// publish RFC 8414 metadata directly under the resource host. The reactive
// flow must still complete via the direct AS-metadata fallback in resolveAS.
func TestOAuthRoundTripper_FallbackASDirect(t *testing.T) {
	var dcrSeen bool
	var issued string
	var seenBearer string
	var mu sync.Mutex

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// AS metadata at the resource host root (no PRM published anywhere).
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           srv.URL,
			"authorization_endpoint":           srv.URL + "/authorize",
			"token_endpoint":                   srv.URL + "/token",
			"registration_endpoint":            srv.URL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dcrSeen = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dcr-fallback"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		u, _ := url.Parse(q.Get("redirect_uri"))
		rq := u.Query()
		rq.Set("code", "auth-code-fb")
		rq.Set("state", q.Get("state"))
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			http.Error(w, "unsupported grant", http.StatusBadRequest)
			return
		}
		if r.Form.Get("code_verifier") == "" {
			http.Error(w, "missing PKCE verifier", http.StatusBadRequest)
			return
		}
		mu.Lock()
		issued = "access-token-fb"
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-token-fb",
			"token_type":    "Bearer",
			"refresh_token": "refresh-fb",
			"expires_in":    3600,
		})
	})
	// MCP resource path: 401 with Atlassian-style challenge (no resource_metadata).
	mux.HandleFunc("/v1/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer access-token-fb" {
			mu.Lock()
			seenBearer = r.Header.Get("Authorization")
			mu.Unlock()
			_, _ = io.WriteString(w, "ok")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="OAuth", error="invalid_token", error_description="Missing or invalid access token"`)
		http.Error(w, "auth required", http.StatusUnauthorized)
	})

	prev := openBrowser
	openBrowser = func(target string) error {
		go func() {
			c := &http.Client{Timeout: 5 * time.Second}
			resp, err := c.Get(target)
			if err == nil && resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	defer func() { openBrowser = prev }()

	store := &fileStore{dir: filepath.Join(t.TempDir(), "tokens")}
	rt := &oauthRoundTripper{
		base:      http.DefaultTransport,
		store:     store,
		server:    "test-fallback",
		serverURL: srv.URL + "/v1/mcp",
		stderr:    io.Discard,
	}
	client := &http.Client{Transport: rt, Timeout: 30 * time.Second}

	resp, err := client.Get(srv.URL + "/v1/mcp")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("expected 200 ok via direct AS fallback; got status=%d body=%q", resp.StatusCode, string(body))
	}
	mu.Lock()
	defer mu.Unlock()
	if !dcrSeen {
		t.Error("expected DCR call during fallback flow")
	}
	if issued != "access-token-fb" {
		t.Errorf("expected access-token-fb issued, got %q", issued)
	}
	if seenBearer != "Bearer access-token-fb" {
		t.Errorf("MCP did not see fallback bearer: %q", seenBearer)
	}
}

func TestOAuthRoundTripper_RefreshFlow(t *testing.T) {
	var refreshCalls int32
	asMux := http.NewServeMux()
	var mu sync.Mutex
	asMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			http.Error(w, "expected refresh_token grant, got "+r.Form.Get("grant_type"), http.StatusBadRequest)
			return
		}
		if r.Form.Get("refresh_token") != "refresh-old" {
			http.Error(w, "wrong refresh token: "+r.Form.Get("refresh_token"), http.StatusBadRequest)
			return
		}
		mu.Lock()
		refreshCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-token-new",
			"token_type":    "Bearer",
			"refresh_token": "refresh-new",
			"expires_in":    3600,
		})
	})
	as := httptest.NewServer(asMux)
	defer as.Close()

	var seenBearer string
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenBearer = r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	defer mcp.Close()

	store := &fileStore{dir: filepath.Join(t.TempDir(), "tokens")}
	expired := &persistedToken{
		Token: &oauth2.Token{
			AccessToken:  "access-token-old",
			RefreshToken: "refresh-old",
			TokenType:    "Bearer",
			Expiry:       time.Now().Add(-time.Hour),
		},
		TokenEndpoint: as.URL + "/token",
		ClientID:      "preset-client",
	}
	if err := store.Set(context.Background(), "test", mcp.URL, expired); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	rt := &oauthRoundTripper{
		base:      http.DefaultTransport,
		store:     store,
		server:    "test",
		serverURL: mcp.URL,
		stderr:    io.Discard,
	}
	client := &http.Client{Transport: rt, Timeout: 10 * time.Second}
	resp, err := client.Get(mcp.URL + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}

	mu.Lock()
	defer mu.Unlock()
	if refreshCalls != 1 {
		t.Errorf("refresh_token grant POST count = %d, want 1", refreshCalls)
	}
	if seenBearer != "Bearer access-token-new" {
		t.Errorf("MCP saw %q, want Bearer access-token-new", seenBearer)
	}
	pt, err := store.Get(context.Background(), "test")
	if err != nil || pt == nil || pt.Token == nil {
		t.Fatalf("store.Get post-refresh: pt=%+v err=%v", pt, err)
	}
	if pt.Token.AccessToken != "access-token-new" {
		t.Errorf("persisted access_token = %q, want access-token-new", pt.Token.AccessToken)
	}
	if pt.Token.RefreshToken != "refresh-new" {
		t.Errorf("persisted refresh_token = %q, want refresh-new", pt.Token.RefreshToken)
	}
}
