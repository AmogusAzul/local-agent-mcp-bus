package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// loginResult is what a browser login yields: the site's cookies (name to
// value) and the XSRF token its pages carry.
type loginResult struct {
	Cookies map[string]string
	XSRF    string
}

// browserLogin signs in through a browser. visible=false must not show a
// window and gives up after a short time; visible=true opens a window and
// waits for the user, until ctx ends or the window is closed.
type browserLogin interface {
	Login(ctx context.Context, visible bool) (*loginResult, error)
}

var (
	errNoBrowser    = fail("BROWSER_NOT_FOUND", "install Microsoft Edge, Chrome, Chromium or Brave, or set BRIGHTSPACE_BROWSER to its path.", "No Chromium-based browser was found")
	errLoginClosed  = errors.New("the login window was closed before signing in")
	errHeadlessWait = errors.New("no silent sign-in")
)

// findBrowser returns a Chromium-based browser installed on this machine,
// Edge first (it ships with Windows).
func findBrowser() (string, error) {
	if p := os.Getenv("BRIGHTSPACE_BROWSER"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	var cands []string
	switch runtime.GOOS {
	case "windows":
		var edge, others []string
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
			root := os.Getenv(env)
			if root == "" {
				continue
			}
			edge = append(edge, filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`))
			others = append(others,
				filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(root, `BraveSoftware\Brave-Browser\Application\brave.exe`),
			)
		}
		cands = append(edge, others...)
	case "darwin":
		cands = []string{
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	default:
		for _, n := range []string{"microsoft-edge", "google-chrome", "chromium", "chromium-browser", "brave-browser"} {
			if p, err := exec.LookPath(n); err == nil {
				cands = append(cands, p)
			}
		}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errNoBrowser
}

// chromium drives a Chromium-based browser over the DevTools protocol, on a
// profile directory the tool owns (never the user's own profile).
type chromium struct {
	site     string // https://host
	profile  string
	find     func() (string, error)
	silentTO time.Duration // how long the headless attempt may take
}

func (b *chromium) Login(ctx context.Context, visible bool) (*loginResult, error) {
	exe, err := b.find()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(b.profile, 0o700); err != nil {
		return nil, err
	}
	portFile := filepath.Join(b.profile, "DevToolsActivePort")
	os.Remove(portFile)
	args := []string{
		"--user-data-dir=" + b.profile,
		"--remote-debugging-port=0",
		"--no-first-run",
		"--no-default-browser-check",
	}
	if !visible {
		args = append(args, "--headless=new")
	}
	args = append(args, b.site+"/d2l/home")
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return nil, fail("LOGIN_BROWSER", "", "Couldn't start the browser: %v", err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()

	port, wsPath, err := waitPort(ctx, portFile, exited)
	if err != nil {
		cmd.Process.Kill()
		return nil, err
	}
	defer shutdown(cmd, exited, fmt.Sprintf("ws://127.0.0.1:%d%s", port, wsPath))

	if !visible {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.silentTO)
		defer cancel()
	}
	return b.waitHome(ctx, port, exited, visible)
}

func waitPort(ctx context.Context, portFile string, exited <-chan struct{}) (int, string, error) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(portFile); err == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= 2 {
				if p, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil {
					return p, strings.TrimSpace(lines[1]), nil
				}
			}
		}
		select {
		case <-exited:
			return 0, "", fail("LOGIN_BROWSER", "", "The browser exited before it could be controlled")
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return 0, "", fail("LOGIN_BROWSER", "", "The browser didn't open its debugging port")
}

// shutdown closes the browser gracefully, so its cookies are flushed to the
// profile, and kills it if it doesn't leave.
func shutdown(cmd *exec.Cmd, exited <-chan struct{}, browserWS string) {
	if c, err := wsDial(browserWS); err == nil {
		c.writeText([]byte(`{"id":1,"method":"Browser.close"}`))
		c.Close()
	}
	select {
	case <-exited:
	case <-time.After(8 * time.Second):
		cmd.Process.Kill()
		<-exited
	}
}

// waitHome polls the first page until it is on the site's /d2l/home, then
// reads the cookies and the XSRF token.
func (b *chromium) waitHome(ctx context.Context, port int, exited <-chan struct{}, visible bool) (*loginResult, error) {
	host := mustHost(b.site)
	var page *cdp
	defer func() {
		if page != nil {
			page.close()
		}
	}()
	for {
		select {
		case <-exited:
			return nil, errLoginClosed
		case <-ctx.Done():
			if visible {
				return nil, ctx.Err()
			}
			return nil, errHeadlessWait
		case <-time.After(500 * time.Millisecond):
		}
		if page == nil {
			p, err := dialPage(port)
			if err != nil {
				continue
			}
			page = p
		}
		href, err := page.evalString(`location.href`)
		if err != nil {
			page.close()
			page = nil
			continue
		}
		u, err := url.Parse(href)
		if err != nil || u.Host != host || !strings.HasPrefix(u.Path, "/d2l/home") {
			continue
		}
		// The page is the signed-in home. Its XSRF token appears in
		// localStorage a moment after load.
		var xsrf string
		for i := 0; i < 20 && xsrf == ""; i++ {
			xsrf, _ = page.evalString(`localStorage.getItem('XSRF.Token')||''`)
			if xsrf == "" {
				time.Sleep(300 * time.Millisecond)
			}
		}
		cookies, err := page.cookies(b.site)
		if err != nil {
			return nil, err
		}
		if cookies["d2lSessionVal"] == "" && cookies["d2lSecureSessionVal"] == "" {
			continue
		}
		return &loginResult{Cookies: cookies, XSRF: xsrf}, nil
	}
}

func mustHost(site string) string {
	u, err := url.Parse(site)
	if err != nil {
		return site
	}
	return u.Host
}

// cdp is a DevTools protocol session on one page.
type cdp struct {
	ws *wsConn
	id int
}

func dialPage(port int) (*cdp, error) {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var targets []struct {
		Type string `json:"type"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, err
	}
	for _, t := range targets {
		if t.Type == "page" && t.WS != "" {
			ws, err := wsDial(t.WS)
			if err != nil {
				return nil, err
			}
			return &cdp{ws: ws}, nil
		}
	}
	return nil, errors.New("no page yet")
}

func (c *cdp) close() { c.ws.Close() }

func (c *cdp) call(method string, params any) (json.RawMessage, error) {
	c.id++
	id := c.id
	b, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	c.ws.c.SetDeadline(time.Now().Add(15 * time.Second))
	if err := c.ws.writeText(b); err != nil {
		return nil, err
	}
	for {
		raw, err := c.ws.readMsg()
		if err != nil {
			return nil, err
		}
		var m struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &m) != nil || m.ID != id {
			continue // an event, or a late reply
		}
		if m.Error != nil {
			return nil, errors.New(m.Error.Message)
		}
		return m.Result, nil
	}
}

func (c *cdp) evalString(expr string) (string, error) {
	res, err := c.call("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", err
	}
	s, _ := r.Result.Value.(string)
	return s, nil
}

func (c *cdp) cookies(site string) (map[string]string, error) {
	res, err := c.call("Network.getCookies", map[string]any{"urls": []string{site}})
	if err != nil {
		return nil, err
	}
	var r struct {
		Cookies []struct{ Name, Value string } `json:"cookies"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, ck := range r.Cookies {
		out[ck.Name] = ck.Value
	}
	return out, nil
}
