package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// tokenLife is how long a minted bearer token is reused. The mint's reply has
// no lifetime, so this is a conservative guess.
const tokenLife = 50 * time.Minute

// sessionData is what the cache file holds.
type sessionData struct {
	Cookies  map[string]string `json:"cookies"`
	XSRF     string            `json:"xsrf"`
	Token    string            `json:"token"`
	MintedAt time.Time         `json:"minted_at"`
}

// session owns the site cookies, the bearer token minted from them, and the
// browser login that obtains the cookies. The login has no time limit: it
// lives until it finishes or the server exits, while each call waits for it
// at most `wait`.
type session struct {
	site string
	path string // cache file
	br   browserLogin
	hc   *http.Client // never follows redirects: the mint answers one when the session is gone
	wait time.Duration
	now  func() time.Time

	mu      sync.Mutex
	loaded  bool
	data    sessionData
	pending *login
}

type login struct {
	done    chan struct{}
	err     error // set before done closes
	visible bool  // a window was opened (guarded by session.mu)
}

var errExpired = errors.New("session expired")

func newSession(site string) *session {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	root := filepath.Join(dir, "local-agent-mcp-bus")
	return &session{
		site: site,
		path: filepath.Join(root, "brightspace.json"),
		br: &chromium{
			site:     site,
			profile:  filepath.Join(root, "brightspace", "profile"),
			find:     findBrowser,
			silentTO: 25 * time.Second,
		},
		hc: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		wait: 30 * time.Second,
		now:  time.Now,
	}
}

// token returns a valid bearer token, minting one from the saved cookies or,
// failing that, starting or joining the browser login.
func (s *session) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	s.load()
	if s.data.Token != "" && s.now().Sub(s.data.MintedAt) < tokenLife {
		t := s.data.Token
		s.mu.Unlock()
		return t, nil
	}
	if len(s.data.Cookies) > 0 && s.data.XSRF != "" {
		err := s.mint(ctx)
		if err == nil {
			t := s.data.Token
			s.mu.Unlock()
			return t, nil
		}
		if !errors.Is(err, errExpired) {
			s.mu.Unlock()
			return "", err
		}
		s.data = sessionData{}
		os.Remove(s.path)
	}
	l := s.pending
	if l == nil {
		l = s.startLogin()
	}
	s.mu.Unlock()

	timer := time.NewTimer(s.wait)
	defer timer.Stop()
	select {
	case <-l.done:
		if l.err != nil {
			return "", l.err
		}
		return s.token(ctx)
	case <-timer.C:
	case <-ctx.Done():
	}
	s.mu.Lock()
	visible := l.visible
	s.mu.Unlock()
	if visible {
		return "", fail("LOGIN_PENDING", "sign in there (tick \"Stay signed in\"), then retry.", "The login window is open. This request can't run until you sign in")
	}
	return "", fail("LOGIN_PENDING", "retry in a few seconds.", "A silent sign-in is still running")
}

// invalidate forgets a token the API rejected, so the next call mints again.
func (s *session) invalidate(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Token == tok {
		s.data.Token = ""
		s.data.MintedAt = time.Time{}
	}
}

// startLogin begins a login in the background. Called with mu held.
func (s *session) startLogin() *login {
	l := &login{done: make(chan struct{})}
	s.pending = l
	go func() {
		res, err := s.br.Login(context.Background(), false)
		var ce *codeErr
		if err != nil && !errors.As(err, &ce) {
			s.mu.Lock()
			l.visible = true
			s.mu.Unlock()
			res, err = s.br.Login(context.Background(), true)
			if errors.Is(err, errLoginClosed) {
				err = fail("LOGIN_CLOSED", "retry to open the login window again.", "The login window was closed before signing in")
			}
		}
		s.mu.Lock()
		if err == nil {
			s.data = sessionData{Cookies: res.Cookies, XSRF: res.XSRF}
			if x := s.fetchXSRF(context.Background()); x != "" {
				s.data.XSRF = x
			}
			err = s.mint(context.Background())
			if errors.Is(err, errExpired) {
				err = fail("API_ERROR", "retry to sign in again.", "The sign-in didn't produce a usable session")
			}
		}
		l.err = err
		s.pending = nil
		s.mu.Unlock()
		close(l.done)
	}()
	return l
}

func (s *session) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var d sessionData
	if json.Unmarshal(b, &d) == nil {
		s.data = d
	}
}

func (s *session) save() {
	b, err := json.Marshal(s.data)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(s.path), 0o700) != nil {
		return
	}
	os.WriteFile(s.path, b, 0o600)
}

func (s *session) cookieHeader() string {
	names := make([]string, 0, len(s.data.Cookies))
	for n := range s.data.Cookies {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=" + s.data.Cookies[n]
	}
	return strings.Join(parts, "; ")
}

// mint exchanges the cookies for a bearer token. Called with mu held.
func (s *session) mint(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "POST", s.site+"/d2l/lp/auth/oauth2/token", strings.NewReader("scope=*:*:*"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", s.cookieHeader())
	req.Header.Set("X-Csrf-Token", s.data.XSRF)
	resp, err := s.hc.Do(req)
	if err != nil {
		return fail("API_ERROR", "check the network and retry.", "Couldn't reach the site: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if loc := resp.Header.Get("Location"); resp.StatusCode == 401 || resp.StatusCode == 403 || strings.Contains(loc, "sessionExpired") ||
		(resp.StatusCode >= 300 && resp.StatusCode < 400) || strings.Contains(string(body), "sessionExpired=1") {
		return errExpired
	}
	if resp.StatusCode != 200 {
		return fail("API_ERROR", "retry later.", "The token request answered HTTP %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &out) != nil || out.AccessToken == "" {
		return fail("API_ERROR", "", "The token request returned no token")
	}
	s.data.Token = out.AccessToken
	s.data.MintedAt = s.now()
	s.save()
	return nil
}

var xsrfPatterns = []*regexp.Regexp{
	regexp.MustCompile(`XSRF\.Token\\?["']\s*,\s*\\?["']([A-Za-z0-9_-]{16,})\\?["']`),
	regexp.MustCompile(`(?i)<meta[^>]*name=["']d2l-xsrf-token["'][^>]*content=["']([A-Za-z0-9_-]{16,})["']`),
	regexp.MustCompile(`(?i)<meta[^>]*content=["']([A-Za-z0-9_-]{16,})["'][^>]*name=["']d2l-xsrf-token["']`),
}

// fetchXSRF reads the XSRF token from the home page with the saved cookies.
// The browser's localStorage can hold the previous session's token, so the
// page itself is the reliable source. Called with mu held.
func (s *session) fetchXSRF(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, "GET", s.site+"/d2l/home", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Cookie", s.cookieHeader())
	resp, err := s.hc.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	for _, re := range xsrfPatterns {
		if m := re.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return ""
}

func siteHost(site string) string {
	u, _ := url.Parse(site)
	return u.Host
}
