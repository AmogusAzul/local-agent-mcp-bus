package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const pid = "6ab5ce636a16851aaf2a5871"

// fakeOverleaf is an in-memory project behind the endpoints the tool uses.
type fakeOverleaf struct {
	mu      sync.Mutex
	session string
	files   map[string]string // path → content
	ids     map[string]string // path → entity id (folders too)
	next    int
	reqs    []string
	polls   int
}

func newFake() *fakeOverleaf {
	return &fakeOverleaf{session: "s1", files: map[string]string{"main.tex": "\\documentclass{article}\n\\begin{document}\nHi\n\\end{document}\n"}, ids: map[string]string{"": "root", "main.tex": "d0"}}
}

func (f *fakeOverleaf) id() string { f.next++; return fmt.Sprintf("e%d", f.next) }

func (f *fakeOverleaf) pathOf(id string) string {
	for p, i := range f.ids {
		if i == id {
			return p
		}
	}
	return "?"
}

func (f *fakeOverleaf) folderJSON(prefix string) folderJSON {
	fj := folderJSON{ID: f.ids[strings.TrimSuffix(prefix, "/")]}
	for p, id := range f.ids {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || rest == "" || strings.Contains(rest, "/") {
			continue
		}
		if _, isFile := f.files[p]; isFile {
			fj.Docs = append(fj.Docs, leafJSON{id, rest})
		} else {
			sub := f.folderJSON(p + "/")
			sub.Name = rest
			fj.Folders = append(fj.Folders, sub)
		}
	}
	return fj
}

func (f *fakeOverleaf) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	ck, _ := r.Cookie(sessionName)
	if ck == nil || ck.Value != f.session {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if r.Method != "GET" && !strings.HasPrefix(r.URL.Path, "/socket.io") && r.Header.Get("X-Csrf-Token") != "tok" {
		http.Error(w, "csrf", 403)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionName, Value: f.session})
	p := r.URL.Path
	switch {
	case p == "/project":
		blob, _ := json.Marshal(map[string]any{"totalSize": 1, "projects": []map[string]any{{"id": pid, "name": "Thesis", "lastUpdated": "2026-11-05T20:27:53.850Z", "accessLevel": "owner"}}})
		fmt.Fprintf(w, `<meta name="ol-usersEmail" content="me@x.co"><meta name="ol-csrfToken" content="tok"><meta name="ol-prefetchedProjectsBlob" data-type="json" content="%s">`, html.EscapeString(string(blob)))
	case p == "/project/new":
		fmt.Fprintf(w, `{"project_id":%q}`, pid)
	case p == "/project/"+pid+"/download/zip":
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, c := range f.files {
			fw, _ := zw.Create(name)
			io.WriteString(fw, c)
		}
		zw.Close()
		w.Write(buf.Bytes())
	case p == "/socket.io/1/":
		io.WriteString(w, "sid1:60:60:websocket,xhr-polling")
	case p == "/socket.io/1/xhr-polling/sid1":
		if r.Method == "POST" {
			return
		}
		f.polls++
		if f.polls%2 == 1 {
			io.WriteString(w, "1::")
			return
		}
		ev, _ := json.Marshal(map[string]any{"name": "joinProjectResponse", "args": []any{map[string]any{"project": map[string]any{"rootFolder": []any{f.folderJSON("")}}}}})
		io.WriteString(w, "5:::"+string(ev))
	case p == "/project/"+pid+"/folder":
		var in struct {
			ParentFolderID string `json:"parent_folder_id"`
			Name           string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		dir := f.pathOf(in.ParentFolderID)
		path := strings.TrimPrefix(dir+"/"+in.Name, "/")
		id := f.id()
		f.ids[path] = id
		fmt.Fprintf(w, `{"_id":%q,"name":%q}`, id, in.Name)
	case p == "/project/"+pid+"/upload":
		dir := f.pathOf(r.URL.Query().Get("folder_id"))
		file, hdr, err := r.FormFile("qqfile")
		if err != nil {
			http.Error(w, "no file", 400)
			return
		}
		b, _ := io.ReadAll(file)
		path := strings.TrimPrefix(dir+"/"+hdr.Filename, "/")
		if _, ok := f.ids[path]; !ok {
			f.ids[path] = f.id()
		}
		f.files[path] = string(b)
		io.WriteString(w, `{"success":true}`)
	case r.Method == "DELETE" && strings.HasPrefix(p, "/project/"+pid+"/"):
		parts := strings.Split(p, "/")
		path := f.pathOf(parts[len(parts)-1])
		delete(f.files, path)
		delete(f.ids, path)
		w.WriteHeader(204)
	case p == "/project/"+pid+"/compile":
		fmt.Fprintf(w, `{"status":"failure","clsiServerId":"c1","outputFiles":[{"path":"output.log","url":"/project/%s/build/1/output/output.log"},{"path":"output.pdf","url":"/project/%s/build/1/output/output.pdf"}]}`, pid, pid)
	case strings.HasSuffix(p, "/output/output.log"):
		if r.URL.Query().Get("clsiserverid") != "c1" {
			http.Error(w, "no clsi", 404)
			return
		}
		io.WriteString(w, "(./main.tex\n./main.tex:3: Undefined control sequence.\nl.3 \\foo\n\nLaTeX Warning: Reference `x' undefined.\n")
	case strings.HasSuffix(p, "/output/output.pdf"):
		io.WriteString(w, "%PDF-1.5 fake")
	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T) (*server, *fakeOverleaf) {
	t.Helper()
	f := newFake()
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	dir := t.TempDir()
	t.Setenv(envCookie, "")
	s := &server{base: ts.URL, root: filepath.Join(dir, "mirrors"), accounts: filepath.Join(dir, "conf", "overleaf.json"), clients: map[string]*client{}, open: func(string) error { return nil }}
	return s, f
}

func call(t *testing.T, fn func(context.Context, map[string]string) (string, error), args map[string]string) string {
	t.Helper()
	out, err := fn(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return out
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ce *codeErr
	if !errors.As(err, &ce) || ce.code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestEndToEnd(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()

	_, err := s.projects(ctx, nil)
	wantCode(t, err, "NOT_LOGGED_IN")
	_, err = s.login(ctx, map[string]string{"cookie": "wrong"})
	wantCode(t, err, "AUTH_EXPIRED")

	out := call(t, s.login, map[string]string{"cookie": "overleaf_session2=s1"})
	if !strings.Contains(out, "me@x.co") || strings.Contains(out, "s1") {
		t.Fatalf("login: %q", out)
	}
	if b, _ := os.ReadFile(s.accounts); !strings.Contains(string(b), `"me@x.co": "s1"`) {
		t.Fatalf("store: %s", b)
	}
	if out := call(t, s.projects, nil); !strings.Contains(out, "Thesis · id:"+pid+" · updated 2026-11-05") {
		t.Fatalf("projects: %q", out)
	}

	// First file access pulls the mirror.
	res, err := s.read(ctx, map[string]string{"project": "thesis", "path": "main.tex", "lines": "3"})
	if err != nil || res.Text != "[main.tex lines 3-3 of 5]\nHi" {
		t.Fatalf("read: %q %v", res.Text, err)
	}
	m, _ := findMirror(s.root, pid)
	if m == nil || filepath.Base(m.dir) != "thesis-2a5871" || m.snap.Account != "me@x.co" {
		t.Fatalf("mirror: %+v", m)
	}

	_, err = s.edit(ctx, map[string]string{"project": pid, "path": "main.tex", "old": "x", "new": "y"})
	wantCode(t, err, "INVALID_ARG")
	call(t, s.edit, map[string]string{"project": pid, "path": "main.tex", "old": "Hi", "new": "Hello \\foo"})
	call(t, s.write, map[string]string{"project": pid, "path": "sec/a/intro.tex", "content": "Intro"})
	if out := call(t, s.status, map[string]string{"project": pid}); out != "M main.tex\nA sec/a/intro.tex\n" {
		t.Fatalf("status: %q", out)
	}
	_, err = s.pull(ctx, map[string]string{"project": pid})
	wantCode(t, err, "LOCAL_CHANGES")

	// Someone edits main.tex on Overleaf: push must stop.
	f.mu.Lock()
	f.files["main.tex"] += "%remote\n"
	f.mu.Unlock()
	_, err = s.push(ctx, map[string]string{"project": pid})
	wantCode(t, err, "REMOTE_CHANGED")
	if res, err := s.read(ctx, map[string]string{"project": pid, "path": ".overleaf-output/remote/main.tex"}); err != nil || !strings.HasSuffix(res.Text, "%remote\n") {
		t.Fatalf("remote copy: %q %v", res.Text, err)
	}
	// Pushing only the new file is fine; folders are created.
	if out := call(t, s.push, map[string]string{"project": pid, "paths": "sec/a/intro.tex"}); out != "Pushed 1: sec/a/intro.tex." {
		t.Fatalf("push: %q", out)
	}
	if out := call(t, s.push, map[string]string{"project": pid, "force": "true"}); out != "Pushed 1: main.tex." {
		t.Fatalf("force push: %q", out)
	}
	f.mu.Lock()
	if f.files["sec/a/intro.tex"] != "Intro" || !strings.Contains(f.files["main.tex"], "Hello") || f.ids["sec"] == "" {
		t.Fatalf("remote: %v %v", f.files, f.ids)
	}
	f.mu.Unlock()
	if out := call(t, s.status, map[string]string{"project": pid}); !strings.HasPrefix(out, "No local changes") {
		t.Fatalf("status after push: %q", out)
	}

	if out := call(t, s.deleteFile, map[string]string{"project": pid, "path": "sec/a/intro.tex"}); !strings.Contains(out, "on Overleaf and locally") {
		t.Fatalf("delete: %q", out)
	}
	if _, ok := f.files["sec/a/intro.tex"]; ok {
		t.Fatal("remote file not deleted")
	}
	if _, err := os.Stat(m.abs("sec/a/intro.tex")); err == nil {
		t.Fatal("local file not deleted")
	}

	res, err = s.compile(ctx, map[string]string{"project": pid, "attach": "true"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Compile: failure", "main.tex:3: Undefined control sequence.", "Warnings: 1", "output.pdf (13 B)", "/project/" + pid} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("compile missing %q:\n%s", want, res.Text)
		}
	}
	if len(res.Blobs) != 1 || res.Blobs[0].MIMEType != "application/pdf" {
		t.Fatalf("blobs: %+v", res.Blobs)
	}
	// Output files are not project files.
	if out := call(t, s.status, map[string]string{"project": pid}); !strings.HasPrefix(out, "No local changes") {
		t.Fatalf("status after compile: %q", out)
	}

	// The session ends.
	f.mu.Lock()
	f.session = "s2"
	f.mu.Unlock()
	_, err = s.projects(ctx, nil)
	wantCode(t, err, "AUTH_EXPIRED")
}

func TestRolledCookieSaved(t *testing.T) {
	s, _ := setup(t)
	call(t, s.login, map[string]string{"cookie": "s1"})
	c, _, _ := s.client("")
	c.keepCookies(&http.Response{Header: http.Header{"Set-Cookie": {sessionName + "=s9; Path=/"}}})
	a, _ := loadAccounts(s.accounts)
	if a.Cookies["me@x.co"] != "s9" {
		t.Fatalf("cookie not rolled: %v", a.Cookies)
	}
}

func TestAccounts(t *testing.T) {
	s, _ := setup(t)
	call(t, s.login, map[string]string{"cookie": "s1"})
	a, _ := loadAccounts(s.accounts)
	a.Cookies["b@y.co"] = "zz"
	a.save(s.accounts)
	if out := call(t, s.login, map[string]string{}); out != "b@y.co\nme@x.co (active)\n" {
		t.Fatalf("list: %q", out)
	}
	call(t, s.login, map[string]string{"account": "b@y.co"})
	if out := call(t, s.logout, map[string]string{}); out != "Logged out b@y.co. Active account: me@x.co." {
		t.Fatalf("logout: %q", out)
	}
	_, err := s.login(context.Background(), map[string]string{"account": "nobody"})
	wantCode(t, err, "NOT_FOUND")

	t.Setenv(envCookie, "envcookie")
	empty := &accounts{Cookies: map[string]string{}}
	if n, c, err := empty.cookie(""); n != "env" || c != "envcookie" || err != nil {
		t.Fatalf("env fallback: %q %q %v", n, c, err)
	}
}

func TestParseDashboard(t *testing.T) {
	page := `<meta name="ol-csrfToken" content="abc"><meta name="ol-usersEmail" content="a&#64;b.co"><meta name="ol-prefetchedProjectsBlob" data-type="json" content="{&quot;totalSize&quot;:30,&quot;projects&quot;:[{&quot;id&quot;:&quot;1&quot;,&quot;name&quot;:&quot;A &amp; B&quot;,&quot;trashed&quot;:true}]}">`
	d, csrf, total, err := parseDashboard(page)
	if err != nil || csrf != "abc" || total != 30 || d.Email != "a@b.co" || d.Projects[0].Name != "A & B" || !d.Projects[0].Trashed {
		t.Fatalf("%+v %q %d %v", d, csrf, total, err)
	}
	_, _, _, err = parseDashboard(`<form action="/login">`)
	wantCode(t, err, "AUTH_EXPIRED")
}

func TestParseFrames(t *testing.T) {
	ev := `5:::{"name":"joinProjectResponse","args":[{"project":{"rootFolder":[{"_id":"r","docs":[{"_id":"d","name":"main.tex"}],"fileRefs":[{"_id":"f","name":"logo.png"}],"folders":[{"_id":"s","name":"sec","docs":[{"_id":"d2","name":"a.tex"}]}]}]}}]}`
	multi := fmt.Sprintf("\ufffd3\ufffd1::\ufffd%d\ufffd%s", len(ev), ev)
	tr, done, err := parseFrames(multi)
	if err != nil || !done {
		t.Fatal(done, err)
	}
	want := tree{"": {"r", "folder"}, "main.tex": {"d", "doc"}, "logo.png": {"f", "file"}, "sec": {"s", "folder"}, "sec/a.tex": {"d2", "doc"}}
	if fmt.Sprint(tr) != fmt.Sprint(want) {
		t.Fatalf("tree %v", tr)
	}
	if _, done, _ := parseFrames("1::"); done {
		t.Fatal("connect frame isn't the tree")
	}
	if _, _, err := parseFrames("7:::not authorized"); err == nil {
		t.Fatal("want error")
	}
}

func TestCleanRel(t *testing.T) {
	for in, want := range map[string]string{"a/b.tex": "a/b.tex", `a\b.tex`: "a/b.tex", "./x/../y.tex": "y.tex", ".overleaf-output/output.pdf": ".overleaf-output/output.pdf"} {
		if got, err := cleanRel(in); got != want || err != nil {
			t.Errorf("%q → %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "/etc/x", "../x", "C:/x", ".overleaf.json", ".git/config"} {
		if _, err := cleanRel(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDiffAndSlug(t *testing.T) {
	c := diff(map[string]string{"a": "1", "b": "2", "c": "3"}, map[string]string{"a": "1", "b": "x", "d": "4"})
	if fmt.Sprint(c) != "{[b] [c] [d]}" {
		t.Fatal(c)
	}
	if slug("Tesis: Capítulo 1 (final)!") != "tesis-cap-tulo-1-final" || slug("¿?") != "project" {
		t.Fatal(slug("Tesis: Capítulo 1 (final)!"))
	}
}

func TestLogErrors(t *testing.T) {
	log := "! Undefined control sequence.\nl.12 \\foo\n\n! Missing $ inserted.\n<inserted text>\n$\nl.20 a_b\nPackage hyperref Warning: Token not allowed.\nLaTeX Warning: Citation `x' undefined.\n"
	errs, warns := logErrors(log, 10)
	if fmt.Sprint(errs) != "[! Undefined control sequence. (l.12) ! Missing $ inserted. (l.20)]" || warns != 2 {
		t.Fatalf("%q %d", errs, warns)
	}
	nested := "(./main.tex (/usr/share/texlive/article.cls\nFile: x.cfg Configuration of (r)epstopdf\n) (./sec/a/intro.tex\n! Undefined control sequence.\nl.1 Intro (with \\foo\n\n)\n! Missing } inserted.\nl.9 }\n"
	if errs, _ := logErrors(nested, 10); fmt.Sprint(errs) != "[sec/a/intro.tex:1: Undefined control sequence. main.tex:9: Missing } inserted.]" {
		t.Fatalf("%q", errs)
	}
	if errs, _ := logErrors("./ch/a.tex:7: LaTeX Error: File `x.sty' not found.\n", 10); fmt.Sprint(errs) != "[ch/a.tex:7: LaTeX Error: File `x.sty' not found.]" {
		t.Fatalf("%q", errs)
	}
}
