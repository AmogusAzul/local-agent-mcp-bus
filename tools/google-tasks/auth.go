package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const scope = "https://www.googleapis.com/auth/tasks"

// Set at build time with -ldflags -X (see Makefile).
var clientID, clientSecret string

// auth owns the refresh token and the browser login that obtains it. The login
// has no time limit: it lives until it finishes or the server exits, while
// each call waits for it at most `wait`.
type auth struct {
	cfg  oauth2.Config
	path string                 // token file
	open func(url string) error // opens the consent page
	wait time.Duration

	mu      sync.Mutex
	ts      oauth2.TokenSource
	pending *login
}

type login struct {
	done    chan struct{}
	err     error // set before done closes
	url     string
	openErr error
}

func newAuth() *auth {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return &auth{
		cfg: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes:       []string{scope},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://accounts.google.com/o/oauth2/auth",
				TokenURL: "https://oauth2.googleapis.com/token",
			},
		},
		path: filepath.Join(dir, "local-agent-mcp-bus", "google-tasks.json"),
		open: openBrowser,
		wait: 30 * time.Second,
	}
}

// openBrowser opens url in the user's default browser. The URL is passed as an
// argument with no shell, so its & needs no escaping.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// client returns an HTTP client authorized for Google Tasks, starting or
// joining the browser login when there is no token.
func (a *auth) client(ctx context.Context) (*http.Client, error) {
	if a.cfg.ClientID == "" {
		return nil, fail("NOT_CONFIGURED", "set GOOGLE_TASKS_CLIENT_ID and GOOGLE_TASKS_CLIENT_SECRET in local.env at the repo root, then make install.", "This google-tasks build has no OAuth client")
	}
	a.mu.Lock()
	if a.ts == nil {
		if tok := a.load(); tok != nil {
			a.setToken(tok)
		}
	}
	if a.ts != nil {
		ts := a.ts
		a.mu.Unlock()
		return &http.Client{Transport: &oauth2.Transport{Source: ts}, Timeout: 30 * time.Second}, nil
	}
	l := a.pending
	if l == nil {
		l = a.start()
	}
	a.mu.Unlock()

	timer := time.NewTimer(a.wait)
	defer timer.Stop()
	select {
	case <-l.done:
		if l.err != nil {
			return nil, l.err
		}
		return a.client(ctx)
	case <-timer.C:
	case <-ctx.Done():
	}
	if l.openErr != nil {
		return nil, fail("LOGIN_BROWSER", "open this URL yourself, log in, then retry: "+l.url, "Couldn't open a browser for the Google login")
	}
	return nil, fail("LOGIN_PENDING", "log in there, then retry.", "The Google login page was opened in your browser. This request can't run until you log in")
}

// invalidate forgets a token Google rejected, so the next call logs in again.
func (a *auth) invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ts = nil
	os.Remove(a.path)
}

func (a *auth) setToken(tok *oauth2.Token) {
	a.ts = oauth2.ReuseTokenSource(tok, a.cfg.TokenSource(context.Background(), tok))
}

func (a *auth) load() *oauth2.Token {
	b, err := os.ReadFile(a.path)
	if err != nil {
		return nil
	}
	var tok oauth2.Token
	if json.Unmarshal(b, &tok) != nil || tok.RefreshToken == "" {
		return nil
	}
	return &tok
}

func (a *auth) save(tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(a.path, b, 0o600)
}

// start begins a loopback login (PKCE) and opens the consent page. Called with mu held.
func (a *auth) start() *login {
	l := &login{done: make(chan struct{})}
	a.pending = l
	finish := func(err error) {
		l.err = err
		a.mu.Lock()
		a.pending = nil
		a.mu.Unlock()
		close(l.done)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		l.err = fail("GOOGLE_ERROR", "", "couldn't listen for the login redirect: %v", err)
		a.pending = nil
		close(l.done)
		return l
	}
	cfg := a.cfg
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	state, verifier := randomString(), oauth2.GenerateVerifier()
	l.url = cfg.AuthCodeURL(state, oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"), oauth2.S256ChallengeOption(verifier))

	var once sync.Once
	srv := &http.Server{}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/" || q.Get("state") != state {
			http.NotFound(w, r)
			return
		}
		err := a.redeem(r.Context(), &cfg, q.Get("code"), q.Get("error"), verifier)
		page := "Logged in to Google Tasks. You can close this tab."
		if err != nil {
			page = "Login failed: " + err.Error()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><title>Google Tasks</title><p style=\"font:16px sans-serif\">%s</p>", page)
		once.Do(func() {
			finish(err)
			go srv.Close()
		})
	})
	go srv.Serve(ln)

	if err := a.open(l.url); err != nil {
		l.openErr = err
	}
	return l
}

// redeem exchanges the authorization code and stores the token.
func (a *auth) redeem(ctx context.Context, cfg *oauth2.Config, code, oauthErr, verifier string) error {
	if oauthErr != "" {
		if oauthErr == "access_denied" {
			return fail("LOGIN_DENIED", "retry and click Allow.", "Access to Google Tasks was denied")
		}
		return fail("GOOGLE_ERROR", "retry the request to log in again.", "Google login failed: %s", oauthErr)
	}
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fail("GOOGLE_ERROR", "retry the request to log in again.", "Google login failed: %v", err)
	}
	if s, ok := tok.Extra("scope").(string); ok && !strings.Contains(s, scope) {
		return fail("LOGIN_SCOPE", "retry and keep the Google Tasks checkbox ticked.", "The login didn't grant access to Google Tasks")
	}
	if tok.RefreshToken == "" {
		return fail("GOOGLE_ERROR", "retry the request to log in again.", "Google returned no refresh token")
	}
	if err := a.save(tok); err != nil {
		return fail("GOOGLE_ERROR", "", "couldn't save the Google token: %v", err)
	}
	a.mu.Lock()
	a.setToken(tok)
	a.mu.Unlock()
	return nil
}

// isInvalidGrant reports whether err means the refresh token was revoked or expired.
func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	return errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || strings.Contains(string(re.Body), "invalid_grant"))
}

func randomString() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
