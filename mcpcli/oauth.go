package mcpcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// parseResourceMetadataURL extracts the `resource_metadata` parameter from a
// `WWW-Authenticate: Bearer ...` challenge per RFC 9728.
//
// Examples accepted:
//
//	Bearer resource_metadata="https://example.com/.well-known/oauth-protected-resource"
//	Bearer error="invalid_token", resource_metadata="https://x/.well-known/oauth-protected-resource"
//	Bearer resource_metadata=https://x/.well-known/oauth-protected-resource
func parseResourceMetadataURL(challenge string) string {
	const wantScheme = "bearer"
	c := strings.TrimSpace(challenge)
	if len(c) < len(wantScheme) {
		return ""
	}
	if !strings.EqualFold(c[:len(wantScheme)], wantScheme) {
		return ""
	}
	rest := strings.TrimSpace(c[len(wantScheme):])
	for rest != "" {
		// param name
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			break
		}
		name := strings.TrimSpace(rest[:eq])
		rest = rest[eq+1:]
		// param value: quoted or token until comma
		var value string
		rest = strings.TrimLeft(rest, " \t")
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				break
			}
			value = rest[1 : 1+end]
			rest = rest[1+end+1:]
		} else {
			comma := strings.IndexByte(rest, ',')
			if comma < 0 {
				value = strings.TrimSpace(rest)
				rest = ""
			} else {
				value = strings.TrimSpace(rest[:comma])
				rest = rest[comma+1:]
			}
		}
		// strip a leading comma + whitespace before the next param
		rest = strings.TrimLeft(rest, ", \t")
		if strings.EqualFold(strings.TrimSpace(name), "resource_metadata") {
			return value
		}
	}
	return ""
}

// resourceMetadata mirrors the subset of RFC 9728 fields that we use.
type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

// asMetadata mirrors the subset of RFC 8414 + OIDC-discovery fields we use.
type asMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	ScopesSupported               []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

func fetchPRM(ctx context.Context, prmURL string) (*resourceMetadata, error) {
	body, err := httpGetJSON(ctx, prmURL)
	if err != nil {
		return nil, fmt.Errorf("fetch PRM %s: %w", prmURL, err)
	}
	var m resourceMetadata
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode PRM %s: %w", prmURL, err)
	}
	if len(m.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("PRM %s has no authorization_servers", prmURL)
	}
	return &m, nil
}

// asMetadataCandidates returns the candidate well-known URLs to probe for an
// issuer, in order. It implements RFC 8414 §3 (insert the well-known path
// between host and path component) and OIDC discovery (append). A
// trailing-slash form is included as a final fallback for servers that only
// publish at the simpler appended location.
func asMetadataCandidates(issuer string) []string {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		// Fall back to dumb concatenation; the discovery may still succeed
		// for issuers that don't use a path component.
		base := strings.TrimRight(issuer, "/")
		return []string{
			base + "/.well-known/oauth-authorization-server",
			base + "/.well-known/openid-configuration",
		}
	}
	path := strings.TrimSuffix(u.Path, "/")
	build := func(p string) string {
		c := *u
		c.Path = p
		c.RawQuery = ""
		c.Fragment = ""
		return c.String()
	}
	return []string{
		build("/.well-known/oauth-authorization-server" + path), // RFC 8414
		build(path + "/.well-known/openid-configuration"),       // OIDC discovery
		build(path + "/.well-known/oauth-authorization-server"), // appended-form compat
	}
}

// fetchASMetadata tries each well-known candidate URL until one yields a
// document with the endpoints we need.
func fetchASMetadata(ctx context.Context, issuer string) (*asMetadata, error) {
	var lastErr error
	for _, u := range asMetadataCandidates(issuer) {
		body, err := httpGetJSON(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		var m asMetadata
		if err := json.Unmarshal(body, &m); err != nil {
			lastErr = fmt.Errorf("decode AS metadata %s: %w", u, err)
			continue
		}
		if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
			lastErr = fmt.Errorf("AS metadata %s missing required endpoints", u)
			continue
		}
		return &m, nil
	}
	return nil, fmt.Errorf("discover AS metadata for %s: %w", issuer, lastErr)
}

// dcrRequest is the RFC 7591 client metadata payload we send.
type dcrRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type dcrResponse struct {
	ClientID string `json:"client_id"`
}

func registerClient(ctx context.Context, registrationEndpoint, redirectURI string) (string, error) {
	payload, _ := json.Marshal(dcrRequest{
		ClientName:              clientName,
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build DCR request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("DCR request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("DCR %s returned %d: %s", registrationEndpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r dcrResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("decode DCR response: %w", err)
	}
	if r.ClientID == "" {
		return "", fmt.Errorf("DCR response missing client_id")
	}
	return r.ClientID, nil
}

func httpGetJSON(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	return body, nil
}

// callbackResult carries either the auth code or an error from the one-shot
// localhost server that handles the redirect.
type callbackResult struct {
	code string
	err  error
}

// runAuthCodeFlow performs the interactive Authorization Code + PKCE leg.
// stderr receives the "open this URL" hint when browser launch fails.
func runAuthCodeFlow(ctx context.Context, cfg *oauth2.Config, listener net.Listener, redirectURI string, stderr io.Writer) (*oauth2.Token, error) {
	state, err := randomString(24)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	verifier := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.AccessTypeOffline)

	resCh := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	cbPath := "/callback"
	if u, err := url.Parse(redirectURI); err == nil && u.Path != "" {
		cbPath = u.Path
	}
	mux.HandleFunc(cbPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			http.Error(w, "authorization error: "+errParam, http.StatusBadRequest)
			resCh <- callbackResult{err: fmt.Errorf("authorization error: %s: %s", errParam, q.Get("error_description"))}
			return
		}
		if got := q.Get("state"); got != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resCh <- callbackResult{err: errors.New("oauth callback state mismatch")}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resCh <- callbackResult{err: errors.New("oauth callback missing code")}
			return
		}
		_, _ = io.WriteString(w, "Authorization complete. You can close this tab.\n")
		resCh <- callbackResult{code: code}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = server.Shutdown(shutCtx)
	}()

	if err := openBrowser(authURL); err != nil {
		fmt.Fprintf(stderr, "Open this URL in your browser to authorize:\n  %s\n", authURL)
	} else {
		fmt.Fprintf(stderr, "Opening browser for authorization (server: %s)...\n", cfg.Endpoint.AuthURL)
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			return nil, r.err
		}
		tok, err := cfg.Exchange(ctx, r.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("token exchange: %w", err)
		}
		return tok, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// openBrowser launches the system browser at target. Indirected via a
// package-level variable so tests can replace the side effect with an HTTP
// GET that drives the authorization endpoint directly.
var openBrowser = func(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

// startCallbackListener listens on 127.0.0.1:port (port=0 → ephemeral) and
// returns the listener and the redirect_uri that should be advertised.
func startCallbackListener(port int) (net.Listener, string, error) {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("listen for oauth callback: %w", err)
	}
	tcp, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return nil, "", fmt.Errorf("unexpected listener address %T", l.Addr())
	}
	redirect := "http://127.0.0.1:" + strconv.Itoa(tcp.Port) + "/callback"
	return l, redirect, nil
}

// oauthFlowParams collects every input the reactive flow needs.
type oauthFlowParams struct {
	PRMURL       string
	CallbackPort int
	ClientID     string // pre-registered, optional
	Stderr       io.Writer
}

// performInteractiveFlow runs the full reactive sequence: PRM → AS metadata
// → DCR (if needed) → PKCE auth code → token. Returns a persistedToken ready
// to be saved.
func performInteractiveFlow(ctx context.Context, p oauthFlowParams) (*persistedToken, error) {
	prm, err := fetchPRM(ctx, p.PRMURL)
	if err != nil {
		return nil, err
	}
	asMeta, err := fetchASMetadata(ctx, prm.AuthorizationServers[0])
	if err != nil {
		return nil, err
	}
	listener, redirect, err := startCallbackListener(p.CallbackPort)
	if err != nil {
		return nil, err
	}
	defer listener.Close()

	clientID := p.ClientID
	if clientID == "" {
		if asMeta.RegistrationEndpoint == "" {
			return nil, fmt.Errorf("server requires OAuth but offers no registration_endpoint and --oauth-client-id is unset")
		}
		clientID, err = registerClient(ctx, asMeta.RegistrationEndpoint, redirect)
		if err != nil {
			return nil, err
		}
	}
	cfg := &oauth2.Config{
		ClientID:    clientID,
		RedirectURL: redirect,
		Endpoint: oauth2.Endpoint{
			AuthURL:  asMeta.AuthorizationEndpoint,
			TokenURL: asMeta.TokenEndpoint,
		},
		Scopes: asMeta.ScopesSupported,
	}
	tok, err := runAuthCodeFlow(ctx, cfg, listener, redirect, p.Stderr)
	if err != nil {
		return nil, err
	}
	return &persistedToken{
		Token:         tok,
		TokenEndpoint: asMeta.TokenEndpoint,
		AuthURL:       asMeta.AuthorizationEndpoint,
		ClientID:      clientID,
		Scopes:        asMeta.ScopesSupported,
	}, nil
}
