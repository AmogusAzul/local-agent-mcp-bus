package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultBase = "https://www.overleaf.com"
	userAgent   = "local-agent-mcp-bus-overleaf/0.1"
	sessionName = "overleaf_session2"
)

// client talks to Overleaf's web endpoints as one logged-in account. It keeps
// its own cookies (the session plus routing cookies such as GCLB) and reports a
// rolled session cookie through onSession so it can be saved.
type client struct {
	base      string
	hc        *http.Client
	onSession func(string)

	mu      sync.Mutex
	cookies map[string]string
	csrf    string
}

func newClient(base, session string, onSession func(string)) *client {
	return &client{
		base: base,
		hc: &http.Client{
			Timeout: 45 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if strings.HasPrefix(req.URL.Path, "/login") || len(via) > 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		onSession: onSession,
		cookies:   map[string]string{sessionName: session},
	}
}

func (c *client) cookieHeader() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := make([]string, 0, len(c.cookies))
	for k, v := range c.cookies {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func (c *client) keepCookies(resp *http.Response) {
	c.mu.Lock()
	rolled := ""
	for _, ck := range resp.Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			continue // never drop the session on a clear; a bad session shows as AUTH_EXPIRED
		}
		if ck.Name == sessionName && ck.Value != c.cookies[sessionName] {
			rolled = ck.Value
		}
		c.cookies[ck.Name] = ck.Value
	}
	c.mu.Unlock()
	if rolled != "" && c.onSession != nil {
		c.onSession(rolled)
	}
}

// do sends a request and returns the body of a 2xx answer. A redirect to the
// login page or a 401 is AUTH_EXPIRED; other failures are OVERLEAF_ERROR.
func (c *client) do(ctx context.Context, method, path string, body io.Reader, contentType string, csrf bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cookie", c.cookieHeader())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if csrf {
		tok, err := c.csrfToken(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Csrf-Token", tok)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Referer", c.base+"/project")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fail("OVERLEAF_ERROR", "check the network and retry.", "%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	c.keepCookies(resp)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fail("OVERLEAF_ERROR", "", "%s %s: %v", method, path, err)
	}
	loc := resp.Header.Get("Location")
	switch {
	case resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.Contains(loc, "/login"):
		return nil, errAuthExpired
	case resp.StatusCode >= 300:
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 || strings.HasPrefix(msg, "<") {
			msg = ""
		}
		return nil, fail("OVERLEAF_ERROR", "", "%s %s: HTTP %d %s", method, path, resp.StatusCode, msg)
	}
	return b, nil
}

func (c *client) postJSON(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	b, err := c.do(ctx, "POST", path, bytes.NewReader(body), "application/json", true)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fail("OVERLEAF_ERROR", "", "POST %s: unexpected answer", path)
	}
	return nil
}

func (c *client) csrfToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	tok := c.csrf
	c.mu.Unlock()
	if tok != "" {
		return tok, nil
	}
	if _, err := c.dashboard(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.csrf, nil
}

type project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	LastUpdated string `json:"lastUpdated"`
	AccessLevel string `json:"accessLevel"`
	Trashed     bool   `json:"trashed"`
	Archived    bool   `json:"archived"`
}

type dashboardData struct {
	Email    string
	Projects []project
}

// dashboard loads GET /project: the account email, the CSRF token and the
// project list (fetched in full from /api/project when the page holds a part).
func (c *client) dashboard(ctx context.Context) (*dashboardData, error) {
	b, err := c.do(ctx, "GET", "/project", nil, "", false)
	if err != nil {
		return nil, err
	}
	d, csrf, total, err := parseDashboard(string(b))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.csrf = csrf
	c.mu.Unlock()
	if total > len(d.Projects) {
		var all struct {
			Projects []project `json:"projects"`
		}
		err := c.postJSON(ctx, "/api/project", map[string]any{
			"filters": map[string]any{},
			"page":    map[string]any{"size": total},
			"sort":    map[string]any{"by": "lastUpdated", "order": "desc"},
		}, &all)
		if err == nil {
			d.Projects = all.Projects
		}
	}
	return d, nil
}

var metaRe = regexp.MustCompile(`<meta\s+name="(ol-[A-Za-z]+)"[^>]*?\scontent="([^"]*)"`)

func parseDashboard(page string) (*dashboardData, string, int, error) {
	metas := map[string]string{}
	for _, m := range metaRe.FindAllStringSubmatch(page, -1) {
		metas[m[1]] = html.UnescapeString(m[2])
	}
	blob, ok := metas["ol-prefetchedProjectsBlob"]
	if !ok || metas["ol-csrfToken"] == "" {
		return nil, "", 0, errAuthExpired
	}
	var p struct {
		TotalSize int       `json:"totalSize"`
		Projects  []project `json:"projects"`
	}
	if err := json.Unmarshal([]byte(blob), &p); err != nil {
		return nil, "", 0, fail("OVERLEAF_ERROR", "", "can't read the project list from the dashboard")
	}
	return &dashboardData{Email: metas["ol-usersEmail"], Projects: p.Projects}, metas["ol-csrfToken"], p.TotalSize, nil
}

func (c *client) createProject(ctx context.Context, name string) (string, error) {
	var out struct {
		ProjectID string `json:"project_id"`
	}
	if err := c.postJSON(ctx, "/project/new", map[string]string{"projectName": name, "template": "none"}, &out); err != nil {
		return "", err
	}
	if out.ProjectID == "" {
		return "", fail("OVERLEAF_ERROR", "", "Overleaf didn't return the new project's id")
	}
	return out.ProjectID, nil
}

// download returns every file of the project, keyed by its /-separated path.
func (c *client) download(ctx context.Context, id string) (map[string][]byte, error) {
	b, err := c.do(ctx, "GET", "/project/"+id+"/download/zip", nil, "", false)
	if err != nil {
		return nil, err
	}
	return unzip(b)
}

func unzip(b []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fail("OVERLEAF_ERROR", "", "the project download isn't a zip")
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[f.Name] = data
	}
	return files, nil
}

func (c *client) upload(ctx context.Context, id, folderID, name string, data []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("relativePath", "null")
	w.WriteField("name", name)
	w.WriteField("type", "application/octet-stream")
	fw, err := w.CreateFormFile("qqfile", name)
	if err != nil {
		return err
	}
	fw.Write(data)
	w.Close()
	b, err := c.do(ctx, "POST", "/project/"+id+"/upload?folder_id="+url.QueryEscape(folderID), &body, w.FormDataContentType(), true)
	if err != nil {
		return err
	}
	var out struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(b, &out) == nil && !out.Success {
		return fail("OVERLEAF_ERROR", "", "upload of %s refused: %s", name, out.Error)
	}
	return nil
}

func (c *client) mkdir(ctx context.Context, id, parentID, name string) (string, error) {
	var out struct {
		ID string `json:"_id"`
	}
	err := c.postJSON(ctx, "/project/"+id+"/folder", map[string]string{"parent_folder_id": parentID, "name": name}, &out)
	if err == nil && out.ID == "" {
		err = fail("OVERLEAF_ERROR", "", "Overleaf didn't return the new folder's id")
	}
	return out.ID, err
}

func (c *client) deleteEntity(ctx context.Context, id string, e entity) error {
	_, err := c.do(ctx, "DELETE", "/project/"+id+"/"+e.Type+"/"+e.ID, strings.NewReader("{}"), "application/json", true)
	return err
}

type compileResult struct {
	Status       string `json:"status"`
	ClsiServerID string `json:"clsiServerId"`
	CompileGroup string `json:"compileGroup"`
	OutputFiles  []struct {
		Path string `json:"path"`
		URL  string `json:"url"`
	} `json:"outputFiles"`
}

func (c *client) compile(ctx context.Context, id string) (*compileResult, error) {
	var out compileResult
	err := c.postJSON(ctx, "/project/"+id+"/compile?auto_compile=false", map[string]any{
		"rootDoc_id":                 nil,
		"draft":                      false,
		"check":                      "silent",
		"incrementalCompilesEnabled": true,
		"stopOnFirstError":           false,
	}, &out)
	return &out, err
}

// output fetches one output file ("output.pdf", "output.log") of a compile, or
// nil when the compile didn't produce it.
func (c *client) output(ctx context.Context, r *compileResult, name string) ([]byte, error) {
	for _, f := range r.OutputFiles {
		if f.Path != name {
			continue
		}
		q := url.Values{}
		if r.ClsiServerID != "" {
			q.Set("clsiserverid", r.ClsiServerID)
		}
		if r.CompileGroup != "" {
			q.Set("compileGroup", r.CompileGroup)
		}
		sep := "?"
		if strings.Contains(f.URL, "?") {
			sep = "&"
		}
		return c.do(ctx, "GET", f.URL+sep+q.Encode(), nil, "", false)
	}
	return nil, nil
}

func (c *client) projectURL(id string) string {
	return fmt.Sprintf("%s/project/%s", c.base, id)
}
