package bs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAPIPathAndGet(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	ctx := context.Background()
	for in, want := range map[string]string{
		"le/1/news/":          "/d2l/api/le/1.2/1/news/",
		"/d2l/api/lp/1.1/x":   "/d2l/api/lp/1.1/x",
		"lp/{v}/users/whoami": "/d2l/api/lp/1.1/users/whoami",
		"lp/enrollments/my/":  "/d2l/api/lp/1.1/enrollments/my/",
	} {
		got, err := a.apiPath(ctx, in)
		if err != nil || got != want {
			t.Errorf("%s: got %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"https://evil/x", "zz/1/x", "le/../x", "le"} {
		if _, err := a.apiPath(ctx, bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	long := strings.Repeat("x", 1000)
	site.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/d2l/api/le/1.2/1/news/" {
			return false
		}
		if r.Method != "GET" {
			site.posts = append(site.posts, []byte(r.Method))
		}
		fmt.Fprintf(w, `[{"Title":"t","Body":"%s","q":"%s"}]`, long, r.URL.RawQuery)
		return true
	}
	out, err := a.toolAPI(ctx, map[string]string{"path": "le/1/news/", "query": "showDeleted=true"})
	if err != nil || !strings.Contains(out, `"Title":"t"`) || strings.Contains(out, long) || !strings.Contains(out, "showDeleted=true") {
		t.Fatalf("get %q %v", out, err)
	}
	out, err = a.toolAPI(ctx, map[string]string{"path": "le/1/news/", "method": "post", "body": `{"Title":"x"}`})
	if err != nil || !strings.Contains(out, "Nothing was sent") || len(site.posts) != 0 {
		t.Fatalf("preview %q %v", out, err)
	}
	if _, err := a.toolAPI(ctx, map[string]string{"path": "le/1/news/", "method": "POST", "body": "{bad"}); err == nil {
		t.Fatal("bad JSON accepted")
	}
	out, err = a.toolAPI(ctx, map[string]string{"path": "le/1/news/", "method": "POST", "body": `{"Title":"x"}`, "confirm": "true"})
	if err != nil || len(site.posts) != 1 || !strings.Contains(out, "HTTP 200") {
		t.Fatalf("confirm %q %v", out, err)
	}
	if out, _ := a.toolAPI(ctx, map[string]string{"path": "help"}); !strings.Contains(out, "dropbox/folders") {
		t.Fatal("help")
	}
}

const testPage = `<html><title>Attendance</title><body><p>Register  week 1</p>
<a href="/d2l/other">Other page</a>
<form name="att" method="post" action="/d2l/web/save">
<input type="hidden" name="d2l_referrer" value="abcdefghijklmnop1234">
<input name="note" value="hi &amp; bye">
<input type="checkbox" name="present" value="1">
<select name="status"><option value="P">Present</option><option value="A" selected>Absent</option></select>
<textarea name="comment">old</textarea>
<input type="submit" value="Save">
</form></body></html>`

func TestWebPageAndForm(t *testing.T) {
	a, site, br := newTestAPI(t, true)
	ctx := context.Background()
	var posted []string
	site.extra = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/d2l/web/page":
			if !strings.Contains(r.Header.Get("Cookie"), "d2lSessionVal=good") {
				http.Redirect(w, r, "/d2l/login", 302)
				return true
			}
			io.WriteString(w, testPage)
		case "/d2l/web/save":
			b, _ := io.ReadAll(r.Body)
			posted = append(posted, string(b))
			http.Redirect(w, r, "/d2l/web/page", 302)
		default:
			return false
		}
		return true
	}
	if _, err := a.toolCourses(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Dead cookies redirect to the login page: the tool signs in again.
	a.s.mu.Lock()
	a.s.data.Cookies = map[string]string{"d2lSessionVal": "stale"}
	a.s.mu.Unlock()
	out, err := a.toolWeb(ctx, map[string]string{"url": "/d2l/web/page"})
	if err != nil {
		t.Fatal(err)
	}
	if br.n() != 2 {
		t.Fatalf("want a second login, got %d", br.n())
	}
	for _, want := range []string{"Title: Attendance", "Form 1 name=att POST /d2l/web/save", "note=hi & bye (text)", "status=A (select) options: P:Present, A:Absent", "comment=old (textarea)", "Other page → /d2l/other", "Register week 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if _, err := a.toolWeb(ctx, map[string]string{"url": "https://evil.example/d2l"}); err == nil {
		t.Fatal("another host accepted")
	}
	args := map[string]string{"url": "/d2l/web/page", "form": "att", "fields": "note=new&present=1"}
	out, err = a.toolWeb(ctx, args)
	if err != nil || !strings.Contains(out, "Nothing was sent") || !strings.Contains(out, "note=new") || len(posted) != 0 {
		t.Fatalf("preview %q %v", out, err)
	}
	args["confirm"] = "true"
	out, err = a.toolWeb(ctx, args)
	if err != nil || len(posted) != 1 {
		t.Fatalf("submit %q %v", out, err)
	}
	for _, want := range []string{"note=new", "present=1", "status=A", "comment=old", "d2l_referrer=abcdefghijklmnop1234"} {
		if !strings.Contains(posted[0], want) {
			t.Errorf("posted %q lacks %q", posted[0], want)
		}
	}
	if !strings.Contains(out, "Title: Attendance") {
		t.Fatalf("redirect not followed: %q", out)
	}
}
