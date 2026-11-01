package main

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeGoogle serves GETs from fixed data and records every request.
type fakeGoogle struct {
	mu    sync.Mutex
	lists []taskList // first one is the default
	tasks map[string][]task
	reqs  []string // "METHOD path?query body"
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	rec := r.Method + " " + r.URL.Path
	if q := r.URL.Query(); r.Method != "GET" && len(q) > 0 {
		rec += "?" + q.Encode()
	}
	if len(body) > 0 {
		rec += " " + string(body)
	}
	f.reqs = append(f.reqs, rec)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	out := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case r.Method == "GET" && r.URL.Path == "/users/@me/lists":
		out(map[string]any{"items": f.lists})
	case r.Method == "GET" && r.URL.Path == "/users/@me/lists/@default":
		out(f.lists[0])
	case r.Method == "GET" && len(parts) == 3 && parts[0] == "lists":
		var items []task
		for _, t := range f.tasks[parts[1]] {
			if t.done() && r.URL.Query().Get("showCompleted") != "true" {
				continue
			}
			items = append(items, t)
		}
		out(map[string]any{"items": items})
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "lists":
		var t task
		json.Unmarshal(body, &t)
		t.ID = "new1"
		out(t)
	case r.Method == "POST" && r.URL.Path == "/users/@me/lists":
		var l taskList
		json.Unmarshal(body, &l)
		l.ID = "newlist"
		out(l)
	default:
		w.Write([]byte("{}"))
	}
}

func (f *fakeGoogle) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var m []string
	for _, r := range f.reqs {
		if !strings.HasPrefix(r, "GET ") {
			m = append(m, r)
		}
	}
	return m
}

func fixture() *fakeGoogle {
	return &fakeGoogle{
		lists: []taskList{{"L1", "My Tasks"}, {"L2", "Uni"}},
		tasks: map[string][]task{
			"L1": {
				{ID: "a", Title: "Pay rent", Status: "needsAction", Due: "2026-11-03T00:00:00.000Z", Position: "1"},
				{ID: "b", Title: "Thesis", Status: "needsAction", Position: "2"},
				{ID: "b1", Title: "Write intro", Status: "needsAction", Parent: "b", Position: "1", Notes: "from the outline\nsecond line"},
				{ID: "b2", Title: "Sources", Status: "completed", Parent: "b", Position: "2"},
				{ID: "c", Title: "Pay rent", Status: "completed", Position: "3"},
				{ID: "d", Title: "Call", Status: "needsAction", Position: "4"},
				{ID: "e", Title: "Call", Status: "needsAction", Position: "5"},
			},
			"L2": {{ID: "u1", Title: "Exam", Status: "needsAction", Position: "1"}},
		},
	}
}

func testAPI(t *testing.T, f *fakeGoogle) *api {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := &auth{cfg: oauth2.Config{ClientID: "x"}, ts: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t"})}
	return &api{auth: a, base: srv.URL}
}

func code(err error) string {
	var ce *codeErr
	if errors.As(err, &ce) {
		return ce.code
	}
	return ""
}

func TestParseDue(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-03":       "",
		"2026-11-03T15:00": "UNSUPPORTED_DUE_TIME",
		"2026-11-03 15:00": "UNSUPPORTED_DUE_TIME",
		"tomorrow 3pm":     "UNSUPPORTED_DUE_TIME",
		"tomorrow":         "INVALID_ARG",
		"2026-13-40":       "INVALID_ARG",
	} {
		_, err := parseDue(in)
		if code(err) != want {
			t.Errorf("parseDue(%q) = %v, want %s", in, err, want)
		}
	}
}

func TestFindTask(t *testing.T) {
	ts := fixture().tasks["L1"]
	if got, _ := findTask(ts, "pay RENT", "My Tasks"); got.ID != "a" {
		t.Errorf("open task should win over completed one, got %s", got.ID)
	}
	if got, _ := findTask(ts, "c", "My Tasks"); got.ID != "c" {
		t.Errorf("id lookup got %s", got.ID)
	}
	if _, err := findTask(ts, "Call", "My Tasks"); code(err) != "AMBIGUOUS" || !strings.Contains(err.Error(), "id:d") {
		t.Errorf("want AMBIGUOUS listing ids, got %v", err)
	}
	if _, err := findTask(ts, "nope", "My Tasks"); code(err) != "TASK_NOT_FOUND" {
		t.Errorf("want TASK_NOT_FOUND, got %v", err)
	}
}

func TestListTree(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local) }
	a := testAPI(t, fixture())
	out, err := a.listTool(context.Background(), map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	want := `Today: 2026-11-01 (Sun) · My Tasks · 5 open
[ ] Pay rent · due 2026-11-03 · id:a
[ ] Thesis · id:b
    [ ] Write intro · id:b1
        notes: from the outline…
[ ] Call · id:d
[ ] Call · id:e
`
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	out, _ = a.listTool(context.Background(), map[string]string{"completed": "true", "query": "intro"})
	if !strings.Contains(out, `[ ] Write intro · subtask of "Thesis" · id:b1`) {
		t.Errorf("orphaned subtask should note its parent:\n%s", out)
	}
	out, _ = a.listTool(context.Background(), map[string]string{"due_before": "2026-11-05"})
	if strings.Count(out, "id:") != 1 {
		t.Errorf("due filter should keep only Pay rent:\n%s", out)
	}
	out, _ = a.listTool(context.Background(), map[string]string{"limit": "2"})
	if !strings.Contains(out, "… 3 more") {
		t.Errorf("limit not applied:\n%s", out)
	}
}

func TestLists(t *testing.T) {
	a := testAPI(t, fixture())
	ctx := context.Background()
	out, err := a.listsTool(ctx, map[string]string{})
	if err != nil || !strings.Contains(out, "My Tasks (default) · 5 open · id:L1") || !strings.Contains(out, "Uni · 1 open") {
		t.Errorf("got %q, %v", out, err)
	}
	if _, err := a.listsTool(ctx, map[string]string{"action": "delete", "list": "my tasks"}); code(err) != "NOT_ALLOWED" {
		t.Errorf("deleting default list: %v", err)
	}
	if _, err := a.listsTool(ctx, map[string]string{"action": "clear", "list": "Nope"}); code(err) != "LIST_NOT_FOUND" || !strings.Contains(err.Error(), "My Tasks, Uni") {
		t.Errorf("want LIST_NOT_FOUND with names, got %v", err)
	}
	if _, err := a.listsTool(ctx, map[string]string{"action": "rename"}); code(err) != "INVALID_ARG" {
		t.Errorf("rename without list: %v", err)
	}
}

func TestCreate(t *testing.T) {
	f := fixture()
	a := testAPI(t, f)
	ctx := context.Background()
	out, err := a.createTool(ctx, map[string]string{"title": "Conclusion", "parent": "Thesis", "after": "Write intro", "due": "2026-11-10"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `under "Thesis", due 2026-11-10`) {
		t.Errorf("got %q", out)
	}
	m := f.mutations()
	if len(m) != 1 || !strings.HasPrefix(m[0], "POST /lists/L1/tasks?parent=b&previous=b1 ") || !strings.Contains(m[0], `"due":"2026-11-10T00:00:00.000Z"`) {
		t.Errorf("request %v", m)
	}
	if _, err := a.createTool(ctx, map[string]string{"title": "x", "parent": "Write intro"}); code(err) != "UNSUPPORTED_DEPTH" || !strings.Contains(err.Error(), `"Thesis"`) {
		t.Errorf("want UNSUPPORTED_DEPTH naming Thesis, got %v", err)
	}
	if _, err := a.createTool(ctx, map[string]string{"title": "x", "after": "Write intro"}); code(err) != "INVALID_ARG" {
		t.Errorf("after a non-sibling: %v", err)
	}
	if _, err := a.createTool(ctx, map[string]string{"title": "  "}); code(err) != "INVALID_ARG" {
		t.Errorf("empty title: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	f := fixture()
	a := testAPI(t, f)
	ctx := context.Background()
	out, err := a.updateTool(ctx, map[string]string{"task": "Thesis", "status": "done", "due": "none"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "status open → done") || !strings.Contains(out, "1 open subtask") {
		t.Errorf("got %q", out)
	}
	if m := f.mutations(); len(m) != 1 || m[0] != `PATCH /lists/L1/tasks/b {"due":null,"status":"completed"}` {
		t.Errorf("request %v", m)
	}
	if _, err := a.updateTool(ctx, map[string]string{"task": "Thesis"}); code(err) != "INVALID_ARG" {
		t.Errorf("no fields: %v", err)
	}
	if _, err := a.updateTool(ctx, map[string]string{"task": "Thesis", "status": "finished"}); code(err) != "INVALID_ARG" {
		t.Errorf("bad status: %v", err)
	}
}

func TestMove(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		args map[string]string
		want string // mutation or error code
	}{
		{map[string]string{"task": "Write intro", "after": "top"}, "POST /lists/L1/tasks/b1/move?parent=b"},
		{map[string]string{"task": "Write intro", "parent": "none", "after": "Thesis"}, "POST /lists/L1/tasks/b1/move?previous=b"},
		{map[string]string{"task": "Pay rent", "parent": "Thesis"}, "POST /lists/L1/tasks/a/move?parent=b"},
		{map[string]string{"task": "Pay rent", "to_list": "Uni", "after": "Exam"}, "POST /lists/L1/tasks/a/move?destinationTasklist=L2&previous=u1"},
		{map[string]string{"task": "Thesis", "parent": "Pay rent"}, "UNSUPPORTED_DEPTH"},
		{map[string]string{"task": "Pay rent", "parent": "Write intro"}, "UNSUPPORTED_DEPTH"},
		{map[string]string{"task": "Pay rent", "parent": "Pay rent"}, "INVALID_ARG"},
		{map[string]string{"task": "Pay rent"}, "INVALID_ARG"},
	}
	for _, c := range cases {
		f := fixture()
		a := testAPI(t, f)
		_, err := a.moveTool(ctx, c.args)
		got := code(err)
		if err == nil {
			got = strings.Join(f.mutations(), "; ")
		}
		if got != c.want {
			t.Errorf("move %v: got %q (%v), want %q", c.args, got, err, c.want)
		}
	}
}

func TestDeleteAndAssigned(t *testing.T) {
	f := fixture()
	f.tasks["L1"] = append(f.tasks["L1"], task{ID: "z", Title: "From doc", Status: "needsAction", Position: "9",
		AssignmentInfo: &struct {
			LinkToTask  string `json:"linkToTask"`
			SurfaceType string `json:"surfaceType"`
		}{"https://docs/x", "DOCUMENT"}})
	a := testAPI(t, f)
	ctx := context.Background()
	out, err := a.deleteTool(ctx, map[string]string{"task": "Thesis"})
	if err != nil || out != `Deleted "Thesis" and 2 subtask(s).` {
		t.Errorf("got %q, %v", out, err)
	}
	if m := f.mutations(); len(m) != 3 || m[2] != "DELETE /lists/L1/tasks/b" {
		t.Errorf("subtasks should go first, then the parent: %v", m)
	}
	if _, err := a.deleteTool(ctx, map[string]string{"task": "From doc"}); code(err) != "NOT_ALLOWED" {
		t.Errorf("assigned without confirm: %v", err)
	}
	if _, err := a.updateTool(ctx, map[string]string{"task": "From doc", "notes": "x"}); code(err) != "UNSUPPORTED_ASSIGNED" || !strings.Contains(err.Error(), "https://docs/x") {
		t.Errorf("notes on assigned: %v", err)
	}
	if _, err := a.createTool(ctx, map[string]string{"title": "x", "parent": "From doc"}); code(err) != "UNSUPPORTED_ASSIGNED" {
		t.Errorf("assigned as parent: %v", err)
	}
}

func TestGoogleErrors(t *testing.T) {
	for status, want := range map[int]string{429: "RATE_LIMITED", 412: "CONFLICT", 500: "GOOGLE_ERROR"} {
		if got := code(googleError(status, []byte(`{"error":{"message":"x"}}`))); got != want {
			t.Errorf("%d: got %s, want %s", status, got, want)
		}
	}
	if got := code(googleError(400, []byte(`{"error":{"message":"Recurring tasks can't be moved"}}`))); got != "UNSUPPORTED_RECURRENCE" {
		t.Errorf("recurrence: %s", got)
	}
}

// fakeOAuth is a token endpoint plus a browser that only records what it opened.
type fakeOAuth struct {
	srv     *httptest.Server
	refresh string // response to refresh_token grants
	mu      sync.Mutex
	opened  []string
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	f := &fakeOAuth{refresh: `{"error":"invalid_grant"}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" {
			w.WriteHeader(400)
			w.Write([]byte(f.refresh))
			return
		}
		if r.Form.Get("code_verifier") == "" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600,"scope":"` + scope + `"}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOAuth) auth(t *testing.T) *auth {
	return &auth{
		cfg:  oauth2.Config{ClientID: "id", Scopes: []string{scope}, Endpoint: oauth2.Endpoint{AuthURL: "https://auth.example/", TokenURL: f.srv.URL}},
		path: filepath.Join(t.TempDir(), "tok.json"),
		open: func(u string) error {
			f.mu.Lock()
			f.opened = append(f.opened, u)
			f.mu.Unlock()
			return nil
		},
		wait: 50 * time.Millisecond,
	}
}

// approve plays the user in the browser: it follows the consent URL's redirect.
func (f *fakeOAuth) approve(t *testing.T, extra string) {
	f.mu.Lock()
	u, _ := url.Parse(f.opened[len(f.opened)-1])
	f.mu.Unlock()
	q := u.Query()
	resp, err := http.Get(q.Get("redirect_uri") + "?state=" + q.Get("state") + extra)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestLoginPendingThenApproved(t *testing.T) {
	f := newFakeOAuth(t)
	a := f.auth(t)
	ctx := context.Background()
	if _, err := a.client(ctx); code(err) != "LOGIN_PENDING" {
		t.Fatalf("want LOGIN_PENDING, got %v", err)
	}
	if _, err := a.client(ctx); code(err) != "LOGIN_PENDING" || len(f.opened) != 1 {
		t.Fatalf("second call should reuse the pending login: %v, opened %d", err, len(f.opened))
	}
	f.approve(t, "&code=c")
	if _, err := a.client(ctx); err != nil {
		t.Fatalf("after approval: %v", err)
	}
	if b, err := os.ReadFile(a.path); err != nil || !strings.Contains(string(b), `"refresh_token":"rt"`) {
		t.Fatalf("token not saved: %s %v", b, err)
	}
	// A fresh server process loads the saved token without a login.
	b := f.auth(t)
	b.path = a.path
	if _, err := b.client(ctx); err != nil || len(f.opened) != 1 {
		t.Fatalf("saved token not reused: %v", err)
	}
}

func TestLoginFinishesWhileWaiting(t *testing.T) {
	f := newFakeOAuth(t)
	a := f.auth(t)
	a.wait = 5 * time.Second
	go func() {
		for {
			f.mu.Lock()
			n := len(f.opened)
			f.mu.Unlock()
			if n > 0 {
				f.approve(t, "&code=c")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if _, err := a.client(context.Background()); err != nil {
		t.Fatalf("call should go ahead once the login finishes: %v", err)
	}
}

func TestLoginDeniedAndBrowserFailure(t *testing.T) {
	f := newFakeOAuth(t)
	a := f.auth(t)
	a.client(context.Background())
	f.approve(t, "&error=access_denied")
	if _, err := a.client(context.Background()); code(err) != "LOGIN_PENDING" || len(f.opened) != 2 {
		t.Fatalf("a denied login should be replaced by a new one: %v, opened %d", err, len(f.opened))
	}

	b := f.auth(t)
	b.open = func(string) error { return errors.New("no browser") }
	if _, err := b.client(context.Background()); code(err) != "LOGIN_BROWSER" || !strings.Contains(err.Error(), "https://auth.example/") {
		t.Fatalf("want LOGIN_BROWSER with the URL, got %v", err)
	}
}

func TestRevokedTokenStartsLogin(t *testing.T) {
	f := newFakeOAuth(t)
	a := f.auth(t)
	a.save(&oauth2.Token{AccessToken: "old", RefreshToken: "revoked", Expiry: time.Now().Add(-time.Hour)})
	g := httptest.NewServer(fixture())
	defer g.Close()
	_, err := (&api{auth: a, base: g.URL}).lists(context.Background())
	if code(err) != "LOGIN_PENDING" || len(f.opened) != 1 {
		t.Fatalf("revoked token should start a login: %v, opened %d", err, len(f.opened))
	}
	if _, err := os.Stat(a.path); !os.IsNotExist(err) {
		t.Fatalf("revoked token file should be removed")
	}
}

func TestNotConfigured(t *testing.T) {
	if _, err := (&auth{}).client(context.Background()); code(err) != "NOT_CONFIGURED" {
		t.Fatalf("got %v", err)
	}
}
