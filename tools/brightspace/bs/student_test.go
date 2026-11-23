package bs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newsAPI serves course 1's news: n items, one a day back from yesterday
// (id 1000+i, i days ago), the first with a long body and an attachment.
func newsAPI(t *testing.T, n int) (*api, *fakeSite) {
	a, site, _ := newTestAPI(t, true)
	now := time.Now()
	long := strings.Repeat("x", 520)
	item := func(i int) map[string]any {
		body := fmt.Sprintf("body %d", i)
		if i == 1 {
			body = long
		}
		m := map[string]any{"Id": 1000 + i, "Title": fmt.Sprintf("News %d", i), "IsPublished": true,
			"StartDate": now.AddDate(0, 0, -i).Add(-time.Hour).UTC().Format(time.RFC3339), "Body": map[string]string{"Text": body}}
		if i == 1 {
			m["Attachments"] = []any{map[string]any{"FileId": 1, "FileName": "syllabus.pdf", "Size": 2048}}
		}
		return m
	}
	site.extra = func(w http.ResponseWriter, r *http.Request) bool {
		out := func(v any) { json.NewEncoder(w).Encode(v) }
		switch p := r.URL.Path; {
		case p == "/d2l/api/le/1.2/1/news/":
			var ns []any
			for i := 1; i <= n; i++ {
				ns = append(ns, item(i))
			}
			out(ns)
		case p == "/d2l/api/le/1.2/1/news/1001":
			out(item(1))
		case strings.HasSuffix(p, "/news/"):
			out([]any{})
		default:
			return false
		}
		return true
	}
	return a, site
}

func TestAnnouncementPages(t *testing.T) {
	a, _ := newsAPI(t, 30)
	ctx := context.Background()

	// Default: last 14 days (13 items), cut at 10, and older ones exist.
	out, err := a.toolAnnouncements(ctx, map[string]string{"course": "calc"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "| id ") != 10 || !strings.Contains(out, "News 1 | id 1001") || strings.Contains(out, "News 11 ") {
		t.Fatalf("default page:\n%s", out)
	}
	if !strings.HasSuffix(out, "More: 3 more in this window: limit=13; older ones: offset=14") {
		t.Fatalf("more line:\n%s", out)
	}

	// A large limit shows the whole window.
	out, _ = a.toolAnnouncements(ctx, map[string]string{"course": "calc", "limit": "200"})
	if strings.Count(out, "| id ") != 13 || strings.Contains(out, "more in this window") {
		t.Fatalf("limit=200:\n%s", out)
	}

	// The second page: 14 to 28 days back.
	out, _ = a.toolAnnouncements(ctx, map[string]string{"course": "calc", "offset": "14", "limit": "50"})
	if !strings.Contains(out, "News 14 ") || !strings.Contains(out, "News 27 ") || strings.Contains(out, "News 13 ") || strings.Contains(out, "News 28 ") {
		t.Fatalf("offset=14:\n%s", out)
	}
	if !strings.HasSuffix(out, "More: older ones: offset=28") {
		t.Fatalf("offset=14 more line:\n%s", out)
	}

	// A custom page length is repeated in the hint; the last page has no hint.
	out, _ = a.toolAnnouncements(ctx, map[string]string{"course": "calc", "days": "5", "offset": "5"})
	if !strings.Contains(out, "News 5 ") || !strings.Contains(out, "News 9 ") || !strings.HasSuffix(out, "More: older ones: offset=10 days=5") {
		t.Fatalf("days=5 offset=5:\n%s", out)
	}
	out, _ = a.toolAnnouncements(ctx, map[string]string{"course": "calc", "offset": "40"})
	if out != "No announcements from 54 to 40 days ago." {
		t.Fatalf("empty page: %q", out)
	}

	for _, bad := range []map[string]string{{"limit": "0"}, {"limit": "201"}, {"offset": "-1"}, {"days": "0"}} {
		if _, err := a.toolAnnouncements(ctx, bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestAnnouncementTruncationAndFullRead(t *testing.T) {
	a, _ := newsAPI(t, 2)
	ctx := context.Background()
	out, err := a.toolAnnouncements(ctx, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("x", 500) + "… (20 more characters: brightspace_announcement course=1 id=1001)"
	if !strings.Contains(out, want) || !strings.Contains(out, "News 2 | id 1002\nbody 2") || strings.Contains(out, "More:") {
		t.Fatalf("list:\n%s", out)
	}

	full, err := a.toolAnnouncement(ctx, map[string]string{"course": "1", "id": "1001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "| 202620_MATH_1 | News 1\n"+strings.Repeat("x", 520)+"\n") ||
		!strings.HasSuffix(full, "Attachments:\n  syllabus.pdf (2 KB)") {
		t.Fatalf("full:\n%s", full)
	}
	if _, err := a.toolAnnouncement(ctx, map[string]string{"course": "1", "id": "abc"}); err == nil {
		t.Fatal("bad id accepted")
	}
	if _, err := a.toolAnnouncement(ctx, map[string]string{"course": "1", "id": "5"}); code(err) != "NOT_FOUND" {
		t.Fatalf("missing id: %v", err)
	}
}

// The fake enrollments mix student courses (1, 2) with a taught one (5).
func TestStudentToolsSkipStaffCourses(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	ctx := context.Background()
	note := "Skipped 1 courses where you are staff (monitor/instructor): 202620_ISIS_3. Ask for one by name to include it."
	tools := map[string]func(context.Context, map[string]string) (string, error){
		"due": a.toolDue, "grades": a.toolGrades, "updates": a.toolUpdates,
	}
	for name, tool := range tools {
		out, err := tool(ctx, map[string]string{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.HasSuffix(out, "\n"+note) {
			t.Errorf("%s lacks the note:\n%s", name, out)
		}
	}
	if n := site.count("GET /d2l/api/le/1.2/5/"); n != 0 {
		t.Fatalf("%d requests to the taught course", n)
	}

	// Naming the course still reaches it, without the note.
	out, err := a.toolGrades(ctx, map[string]string{"course": "ISIS"})
	if err != nil || strings.Contains(out, "Skipped") || site.count("GET /d2l/api/le/1.2/5/grades/") != 1 {
		t.Fatalf("named staff course: %q %v", out, err)
	}

	// Announcements keep every current course.
	a.toolAnnouncements(ctx, map[string]string{})
	if site.count("GET /d2l/api/le/1.2/5/news/") != 1 {
		t.Fatal("announcements skipped the taught course")
	}
}
