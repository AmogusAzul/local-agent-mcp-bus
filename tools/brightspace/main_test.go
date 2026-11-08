package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSite is a minimal D2L: cookies d2lSessionVal=good mint a token, the
// token opens the API.
type fakeSite struct {
	mu        sync.Mutex
	reqs      []string
	posts     [][]byte // bodies of submission POSTs, with their content type first
	postTypes []string
	expireTok bool // answer 401 to the current token once
	subs      []map[string]any
	files     map[int64][]byte
}

const goodToken = "tok-good"

func (f *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	out := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/d2l/home":
		if !strings.Contains(r.Header.Get("Cookie"), "d2lSessionVal=good") {
			http.Redirect(w, r, "/d2l/login", 302)
			return
		}
		fmt.Fprint(w, `<script>localStorage.setItem('XSRF.Token','abcdefghijklmnop1234')</script>`)
	case r.URL.Path == "/d2l/lp/auth/oauth2/token":
		if !strings.Contains(r.Header.Get("Cookie"), "d2lSessionVal=good") || r.Header.Get("X-Csrf-Token") != "abcdefghijklmnop1234" {
			http.Error(w, "no", 401)
			return
		}
		out(map[string]string{"access_token": goodToken})
	case strings.HasPrefix(r.URL.Path, "/d2l/api/"):
		if r.Header.Get("Authorization") != "Bearer "+goodToken || f.expireTok {
			f.expireTok = false
			http.Error(w, "expired", 401)
			return
		}
		f.api(w, r, out)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeSite) api(w http.ResponseWriter, r *http.Request, out func(any)) {
	p := r.URL.Path
	soon := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	switch {
	case p == "/d2l/api/versions/":
		out([]map[string]string{{"ProductCode": "lp", "LatestVersion": "1.1"}, {"ProductCode": "le", "LatestVersion": "1.2"}})
	case p == "/d2l/api/lp/1.1/enrollments/myenrollments/":
		item := func(id int, code, name, end, last string) map[string]any {
			return map[string]any{
				"OrgUnit": map[string]any{"Id": id, "Type": map[string]string{"Code": "Course Offering"}, "Name": name, "Code": code},
				"Access":  map[string]any{"IsActive": true, "CanAccess": true, "StartDate": "2020-01-01T00:00:00.000Z", "EndDate": end, "LastAccessed": last},
			}
		}
		out(map[string]any{"PagingInfo": map[string]any{"HasMoreItems": false}, "Items": []any{
			item(1, "202620_MATH_1", "Calculus", time.Now().Add(1000*time.Hour).UTC().Format(time.RFC3339), ""),
			item(2, "202620_MATH_2", "Algebra", time.Now().Add(1000*time.Hour).UTC().Format(time.RFC3339), ""),
			item(3, "202510_OLD_1", "Old course", "2025-06-01T00:00:00.000Z", ""),
			item(4, "AD_STALE", "Undated stale", "", "2024-01-01T00:00:00.000Z"),
		}})
	case p == "/d2l/api/le/1.2/1/dropbox/folders/":
		out([]map[string]any{
			{"Id": 10, "Name": "Homework 1", "DueDate": soon, "SubmissionType": 0},
			{"Id": 11, "Name": "Essay", "DueDate": soon, "SubmissionType": 1},
			{"Id": 12, "Name": "Closed one", "DueDate": soon, "SubmissionType": 0, "Availability": map[string]string{"EndDate": "2020-01-01T00:00:00.000Z"}},
			{"Id": 13, "Name": "Pdf only", "DueDate": soon, "SubmissionType": 0, "CustomAllowableFileTypes": "pdf"},
		})
	case p == "/d2l/api/le/1.2/2/dropbox/folders/":
		out([]any{})
	case strings.HasSuffix(p, "/submissions/mysubmissions/") && r.Method == "GET":
		out([]any{map[string]any{"Submissions": f.subs}})
	case strings.HasSuffix(p, "/submissions/mysubmissions/") && r.Method == "POST":
		b, _ := io.ReadAll(r.Body)
		f.posts = append(f.posts, b)
		f.postTypes = append(f.postTypes, r.Header.Get("Content-Type"))
		f.subs = append(f.subs, map[string]any{"Id": 99, "SubmissionDate": soon, "Files": []map[string]any{{"FileName": "hw.pdf", "Size": 5}}})
		w.WriteHeader(200)
	case strings.HasSuffix(p, "/quizzes/"):
		out(map[string]any{"Objects": []any{map[string]any{"Name": "Quiz A", "DueDate": soon}}})
	case p == "/d2l/api/le/1.2/1/content/toc":
		out(map[string]any{"Modules": []any{map[string]any{"ModuleId": 1, "Title": "Week 1", "Topics": []any{
			map[string]any{"TopicId": 100, "Title": "Notes.pdf", "TypeIdentifier": "File", "Url": "/content/Notes.pdf", "LastModifiedDate": "2026-01-01T00:00:00.000Z"},
			map[string]any{"TopicId": 101, "Title": "Slides.pptx", "TypeIdentifier": "File", "Url": "/content/Slides.pptx", "LastModifiedDate": "2026-01-01T00:00:00.000Z"},
			map[string]any{"TopicId": 102, "Title": "Site", "TypeIdentifier": "Link", "Url": "https://example.com"},
		}}}})
	case strings.Contains(p, "/content/topics/") && strings.HasSuffix(p, "/file"):
		var id int64
		fmt.Sscanf(p[strings.Index(p, "topics/")+7:], "%d", &id)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="file%d.bin"`, id))
		w.Write(f.files[id])
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeSite) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// fakeBrowser signs in by returning the good cookies, after `gate` opens.
type fakeBrowser struct {
	mu     sync.Mutex
	calls  []bool // visible flags
	silent bool   // whether the headless attempt works
	gate   chan struct{}
}

func (b *fakeBrowser) n() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *fakeBrowser) first() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[0]
}

func (b *fakeBrowser) Login(ctx context.Context, visible bool) (*loginResult, error) {
	b.mu.Lock()
	b.calls = append(b.calls, visible)
	b.mu.Unlock()
	if !visible && !b.silent {
		return nil, errHeadlessWait
	}
	if b.gate != nil {
		<-b.gate
	}
	return &loginResult{Cookies: map[string]string{"d2lSessionVal": "good"}}, nil
}

func newTestAPI(t *testing.T, silent bool) (*api, *fakeSite, *fakeBrowser) {
	t.Helper()
	site := &fakeSite{files: map[int64][]byte{100: []byte("pdf-bytes"), 101: []byte("slides")}}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	br := &fakeBrowser{silent: silent}
	s := newSession(srv.URL)
	s.path = filepath.Join(t.TempDir(), "brightspace.json")
	s.br = br
	s.wait = 2 * time.Second
	a := newAPI(s)
	a.jobWait = 5 * time.Second
	return a, site, br
}

func TestSilentLoginMintsAndCaches(t *testing.T) {
	a, site, br := newTestAPI(t, true)
	ctx := context.Background()
	out, err := a.toolCourses(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Calculus") || !strings.Contains(out, "Algebra") {
		t.Fatalf("current courses missing: %q", out)
	}
	if strings.Contains(out, "Old course") || strings.Contains(out, "Undated stale") {
		t.Fatalf("past courses listed as current: %q", out)
	}
	if br.n() != 1 || br.first() {
		t.Fatalf("want one headless login, got %d calls", br.n())
	}
	// A second server process reuses the cache without any login.
	s2 := newSession(a.s.site)
	s2.path = a.s.path
	s2.br = br
	if _, err := newAPI(s2).toolCourses(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if br.n() != 1 {
		t.Fatal("cache not reused")
	}
	if site.count("POST /d2l/lp/auth/oauth2/token") != 1 {
		t.Fatalf("token minted more than once")
	}
	info, _ := os.Stat(a.s.path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode %v", info.Mode().Perm())
	}
}

func TestExpiredTokenRemintsWithoutLogin(t *testing.T) {
	a, site, br := newTestAPI(t, true)
	ctx := context.Background()
	if _, err := a.toolCourses(ctx, nil); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.courses = nil
	a.mu.Unlock()
	site.expireTok = true
	if _, err := a.toolCourses(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if br.n() != 1 {
		t.Fatal("a login ran although the cookies were fine")
	}
	if site.count("POST /d2l/lp/auth/oauth2/token") != 2 {
		t.Fatalf("expected a second mint")
	}
}

func TestDeadCookiesLogInAgain(t *testing.T) {
	a, _, br := newTestAPI(t, true)
	ctx := context.Background()
	if _, err := a.toolCourses(ctx, nil); err != nil {
		t.Fatal(err)
	}
	a.s.mu.Lock()
	a.s.data.Cookies = map[string]string{"d2lSessionVal": "stale"}
	a.s.data.Token = ""
	a.s.mu.Unlock()
	a.mu.Lock()
	a.courses = nil
	a.mu.Unlock()
	if _, err := a.toolCourses(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if br.n() != 2 {
		t.Fatalf("want a second login, got %d logins", br.n())
	}
}

func TestPendingVisibleLogin(t *testing.T) {
	a, _, br := newTestAPI(t, false)
	br.gate = make(chan struct{})
	a.s.wait = 200 * time.Millisecond
	ctx := context.Background()
	_, err := a.toolCourses(ctx, nil)
	ce, ok := err.(*codeErr)
	if !ok || ce.code != "LOGIN_PENDING" {
		t.Fatalf("want LOGIN_PENDING, got %v", err)
	}
	if !strings.Contains(ce.msg, "login window") {
		t.Fatalf("message should say a window is open: %v", err)
	}
	// A retry joins the same login: no second window.
	if _, err := a.toolCourses(ctx, nil); err == nil {
		t.Fatal("still pending, want an error")
	}
	if br.n() != 2 { // headless attempt, then one visible window
		t.Fatalf("a retry opened another window (%d logins)", br.n())
	}
	close(br.gate)
	a.s.wait = 2 * time.Second
	if _, err := a.toolCourses(ctx, nil); err != nil {
		t.Fatalf("after the login finished: %v", err)
	}
	if br.n() != 2 {
		t.Fatalf("finishing the login started another (%d logins)", br.n())
	}
}

func TestNoBrowser(t *testing.T) {
	a, _, _ := newTestAPI(t, false)
	a.s.br = &chromium{find: func() (string, error) { return "", errNoBrowser }}
	_, err := a.toolCourses(context.Background(), nil)
	if ce, ok := err.(*codeErr); !ok || ce.code != "BROWSER_NOT_FOUND" {
		t.Fatalf("got %v", err)
	}
}

func TestCourseMatching(t *testing.T) {
	a, _, _ := newTestAPI(t, true)
	ctx := context.Background()
	c, err := a.resolveCourse(ctx, "calc")
	if err != nil || c.ID != 1 {
		t.Fatalf("calc: %v %v", c, err)
	}
	if _, err := a.resolveCourse(ctx, "math"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("math should be ambiguous: %v", err)
	}
	if c, err := a.resolveCourse(ctx, "OLD"); err != nil || c.ID != 3 {
		t.Fatalf("past course by code: %v %v", c, err)
	}
	if _, err := a.resolveCourse(ctx, "nothing"); err == nil {
		t.Fatal("want NOT_FOUND")
	}
}

func TestDue(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	site.subs = nil
	out, err := a.toolDue(context.Background(), map[string]string{"days": "7"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Homework 1 (assignment, not submitted)", "Quiz A (quiz)", "Essay", "Pdf only"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "Closed one") == false {
		t.Log("closed folder with a future due date is still listed")
	}
	if _, err := a.toolDue(context.Background(), map[string]string{"days": "x"}); err == nil {
		t.Fatal("bad days accepted")
	}
}

func TestSubmitPreviewSendsNothing(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	dir := t.TempDir()
	f := filepath.Join(dir, "hw.pdf")
	os.WriteFile(f, []byte("12345"), 0o644)
	out, err := a.toolSubmit(context.Background(), map[string]string{"course": "calc", "assignment": "homework", "files": f})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing was sent") || !strings.Contains(out, "hw.pdf") {
		t.Fatalf("preview: %s", out)
	}
	if len(site.posts) != 0 {
		t.Fatal("preview posted")
	}
	// Anything but "true" is a preview.
	if _, err := a.toolSubmit(context.Background(), map[string]string{"course": "calc", "assignment": "homework", "files": f, "confirm": "yes"}); err != nil || len(site.posts) != 0 {
		t.Fatalf("confirm=yes posted: %v", err)
	}
}

func TestSubmitConfirm(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	f := filepath.Join(t.TempDir(), "hw.pdf")
	os.WriteFile(f, []byte("12345"), 0o644)
	out, err := a.toolSubmit(context.Background(), map[string]string{
		"course": "calc", "assignment": "10", "files": f, "text": "my comment", "confirm": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "submission 99") || !strings.Contains(out, "hw.pdf") {
		t.Fatalf("read-back missing: %s", out)
	}
	if len(site.posts) != 1 {
		t.Fatalf("posts %d", len(site.posts))
	}
	mt, params, err := mime.ParseMediaType(site.postTypes[0])
	if err != nil || mt != "multipart/mixed" {
		t.Fatalf("content type %q", site.postTypes[0])
	}
	mr := multipart.NewReader(strings.NewReader(string(site.posts[0])), params["boundary"])
	p1, err := mr.NextPart()
	if err != nil || p1.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("first part %v %v", p1, err)
	}
	js, _ := io.ReadAll(p1)
	if !strings.Contains(string(js), `"Text":"my comment"`) {
		t.Fatalf("json part %s", js)
	}
	p2, err := mr.NextPart()
	if err != nil || !strings.Contains(p2.Header.Get("Content-Disposition"), `filename="hw.pdf"`) {
		t.Fatalf("file part %v %v", p2, err)
	}
	if b, _ := io.ReadAll(p2); string(b) != "12345" {
		t.Fatalf("file bytes %q", b)
	}
}

func TestSubmitRefusals(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	dir := t.TempDir()
	pdf, txt := filepath.Join(dir, "a.pdf"), filepath.Join(dir, "a.txt")
	os.WriteFile(pdf, []byte("x"), 0o644)
	os.WriteFile(txt, []byte("x"), 0o644)
	cases := []struct {
		name string
		args map[string]string
		code string
	}{
		{"closed", map[string]string{"assignment": "closed", "files": pdf}, "CLOSED"},
		{"text-only folder with files", map[string]string{"assignment": "essay", "files": pdf}, "FILE_REJECTED"},
		{"files-only folder with text", map[string]string{"assignment": "homework", "text": "hi"}, "FILE_REJECTED"},
		{"missing file", map[string]string{"assignment": "homework", "files": filepath.Join(dir, "nope.pdf")}, "FILE_REJECTED"},
		{"wrong type", map[string]string{"assignment": "pdf only", "files": txt}, "FILE_REJECTED"},
		{"directory", map[string]string{"assignment": "homework", "files": dir}, "FILE_REJECTED"},
		{"nothing to send", map[string]string{"assignment": "homework"}, "INVALID_ARG"},
		{"unknown assignment", map[string]string{"assignment": "zzz", "files": pdf}, "NOT_FOUND"},
	}
	for _, c := range cases {
		c.args["course"], c.args["confirm"] = "calc", "true"
		_, err := a.toolSubmit(context.Background(), c.args)
		ce, ok := err.(*codeErr)
		if !ok || ce.code != c.code {
			t.Errorf("%s: want %s, got %v", c.name, c.code, err)
		}
	}
	if len(site.posts) != 0 {
		t.Fatalf("a refused submission was sent")
	}
	// An allowed type passes the checks.
	if _, err := a.toolSubmit(context.Background(), map[string]string{"course": "calc", "assignment": "pdf only", "files": pdf}); err != nil {
		t.Fatalf("pdf into pdf-only folder: %v", err)
	}
}

func TestContentAndDownload(t *testing.T) {
	a, _, _ := newTestAPI(t, true)
	ctx := context.Background()
	tree, err := a.toolContent(ctx, map[string]string{"course": "calc"})
	if err != nil || !strings.Contains(tree, "Week 1") || !strings.Contains(tree, "[100] Notes.pdf (File)") {
		t.Fatalf("tree %q %v", tree, err)
	}
	link, err := a.toolContent(ctx, map[string]string{"course": "calc", "topic": "Site"})
	if err != nil || !strings.Contains(link, "https://example.com") {
		t.Fatalf("link %q %v", link, err)
	}
	dest := t.TempDir()
	out, err := a.toolDownload(ctx, map[string]string{"course": "calc", "topic": "notes", "dest": dest})
	if err != nil || !strings.Contains(out, "file100.bin") {
		t.Fatalf("download %q %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "file100.bin")); string(b) != "pdf-bytes" {
		t.Fatalf("content %q", b)
	}
	if _, err := a.toolDownload(ctx, map[string]string{"course": "calc", "topic": "Site", "dest": dest}); err == nil {
		t.Fatal("a link topic was downloaded")
	}
}

func TestSyncResumesAndFilters(t *testing.T) {
	a, _, _ := newTestAPI(t, true)
	ctx := context.Background()
	dest := t.TempDir()
	out, err := a.toolSync(ctx, map[string]string{"course": "calc", "dest": dest})
	if err != nil || !strings.Contains(out, "2 downloaded") {
		t.Fatalf("first sync %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Week 1", "file101.bin")); err != nil {
		t.Fatalf("module folder structure: %v", err)
	}
	out, err = a.toolSync(ctx, map[string]string{"course": "calc", "dest": dest})
	if err != nil || !strings.Contains(out, "0 downloaded") || !strings.Contains(out, "2 unchanged") {
		t.Fatalf("second sync %q %v", out, err)
	}
	// A deleted file is fetched again; nothing local is ever deleted.
	os.Remove(filepath.Join(dest, "Week 1", "file100.bin"))
	extra := filepath.Join(dest, "mine.txt")
	os.WriteFile(extra, []byte("keep"), 0o644)
	out, _ = a.toolSync(ctx, map[string]string{"course": "calc", "dest": dest})
	if !strings.Contains(out, "1 downloaded") {
		t.Fatalf("resume %q", out)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatal("sync deleted a local file")
	}
	// Filters.
	d2 := t.TempDir()
	out, _ = a.toolSync(ctx, map[string]string{"course": "calc", "dest": d2, "types": "pptx"})
	if !strings.Contains(out, "1 downloaded") || !strings.Contains(out, "1 skipped") {
		t.Fatalf("types filter %q", out)
	}
}

func TestSyncJobReportsRunning(t *testing.T) {
	a, _, _ := newTestAPI(t, true)
	a.jobWait = time.Millisecond
	j := &job{course: "x", dest: "d", done: make(chan struct{}), total: 10, finished: 3}
	a.jobs.add(j)
	out, err := a.toolSync(context.Background(), map[string]string{"job": j.id})
	if err != nil || !strings.Contains(out, "running: 3/10") || !strings.Contains(out, "job="+j.id) {
		t.Fatalf("%q %v", out, err)
	}
	close(j.done)
	out, _ = a.toolSync(context.Background(), map[string]string{"job": j.id})
	if !strings.Contains(out, "done") {
		t.Fatalf("%q", out)
	}
	if _, err := a.toolSync(context.Background(), map[string]string{"job": "sync-99"}); err == nil {
		t.Fatal("unknown job accepted")
	}
}

func TestFormatting(t *testing.T) {
	if got := plain("\ufeff<p>Hola&nbsp;<b>mundo</b></p><script>x()</script><p>dos &amp; tres</p>"); got != "Hola mundo\ndos & tres" {
		t.Fatalf("plain %q", got)
	}
	if got := safeName(`a/b:c*"d`); got != "a_b_c__d" {
		t.Fatalf("safeName %q", got)
	}
	if got := safeName("  .. "); got != "_" {
		t.Fatalf("safeName empty %q", got)
	}
	if size(1536) != "2 KB" || size(5<<20) != "5.0 MB" {
		t.Fatal("size")
	}
	if tm := parseTime("2026-11-12T19:00:00.000Z"); tm.IsZero() || tm.UTC().Hour() != 19 {
		t.Fatalf("parseTime %v", tm)
	}
}

func TestToolListIsCompact(t *testing.T) {
	ts := newTools(newAPI(newSession("https://x")))
	n := 0
	for _, tl := range ts {
		n += len(tl.Name) + len(tl.Description)
		for _, a := range tl.Args {
			n += len(a.Name) + len(a.Description)
		}
	}
	if n > 2600 {
		t.Fatalf("tool text is %d bytes", n)
	}
	if len(ts) != 8 {
		t.Fatalf("%d tools", len(ts))
	}
}
