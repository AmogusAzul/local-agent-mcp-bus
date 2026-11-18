package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// peopleSite answers class list, group and calendar routes for course 1.
func peopleSite(site *fakeSite, news *[]map[string]any) {
	site.extra = func(w http.ResponseWriter, r *http.Request) bool {
		out := func(v any) { json.NewEncoder(w).Encode(v) }
		switch p := r.URL.Path; {
		case p == "/d2l/api/le/1.2/1/classlist/":
			email := "ana@x.edu"
			out([]any{
				map[string]any{"Identifier": "7", "DisplayName": "Me Myself", "ClasslistRoleDisplayName": "Monitor", "OrgDefinedId": "1"},
				map[string]any{"Identifier": "8", "DisplayName": "Ana Ruiz", "ClasslistRoleDisplayName": "Estudiante", "OrgDefinedId": "2026", "Email": email},
				map[string]any{"Identifier": "9", "DisplayName": "Beto Paz", "ClasslistRoleDisplayName": "Estudiante", "OrgDefinedId": "2027"},
			})
		case p == "/d2l/api/lp/1.1/1/groupcategories/":
			out([]any{map[string]any{"GroupCategoryId": 5, "Name": "Taller 1", "MaxUsersPerGroup": 4, "SelfEnrollmentExpiryDate": "2026-09-03T17:00:00.000Z"}})
		case p == "/d2l/api/le/1.2/calendar/events/myEvents/":
			if r.URL.Query().Get("orgUnitIdsCSV") == "" {
				http.Error(w, "need ids", 400)
				return true
			}
			out(map[string]any{"Objects": []any{
				map[string]any{"Title": "Parcial", "OrgUnitId": 1, "StartDateTime": "2026-11-20T15:00:00.000Z", "EndDateTime": "2026-11-20T17:00:00.000Z", "LocationName": "ML 101"},
			}, "Next": nil})
		case p == "/d2l/api/le/1.2/1/news/":
			out(*news)
		case p == "/d2l/api/le/1.2/1/updates/myUpdates":
			out(map[string]any{"OrgUnitId": "1", "UnreadAssignmentFeedback": 2, "UngradedQuizzes": 0})
		default:
			return false
		}
		return true
	}
}

func TestPeopleAndGroups(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	peopleSite(site, &[]map[string]any{})
	ctx := context.Background()
	out, err := a.toolPeople(ctx, map[string]string{"course": "calc", "role": "estudiante"})
	if err != nil || !strings.Contains(out, "Ana Ruiz (8) | Estudiante | G 2") || strings.Contains(out, "Me Myself") || strings.Contains(out, "ana@x.edu") {
		t.Fatalf("people %q %v", out, err)
	}
	out, _ = a.toolPeople(ctx, map[string]string{"course": "calc", "group": "G 1", "contact": "true"})
	if !strings.Contains(out, "Me Myself") || strings.Contains(out, "Ana") {
		t.Fatalf("group filter %q", out)
	}
	out, _ = a.toolPeople(ctx, map[string]string{"course": "calc", "role": "estud", "contact": "true"})
	if !strings.Contains(out, "ana@x.edu | 2026") {
		t.Fatalf("contact %q", out)
	}
	out, err = a.toolGroups(ctx, map[string]string{"course": "calc"})
	if err != nil || !strings.Contains(out, "Taller 1 (category 5), 2 groups, up to 4 each") || !strings.Contains(out, "G 1 (1): 1/4 (yours)") || strings.Contains(out, "Ana") {
		t.Fatalf("groups %q %v", out, err)
	}
	out, _ = a.toolGroups(ctx, map[string]string{"course": "calc", "category": "taller"})
	if !strings.Contains(out, "G 2 (2): 1/4 — Ana Ruiz") {
		t.Fatalf("members %q", out)
	}
}

func TestCalendar(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	peopleSite(site, &[]map[string]any{})
	out, err := a.toolCalendar(context.Background(), map[string]string{"course": "calc"})
	if err != nil || !strings.Contains(out, "202620_MATH_1 | Parcial @ ML 101") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestUpdatesDiffs(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	news := []map[string]any{{"Id": 1, "Title": "Welcome"}}
	peopleSite(site, &news)
	ctx := context.Background()
	out, err := a.toolUpdates(ctx, map[string]string{"course": "calc"})
	if err != nil || !strings.Contains(out, "First look") || !strings.Contains(out, "UnreadAssignmentFeedback 2") {
		t.Fatalf("first %q %v", out, err)
	}
	news = append(news, map[string]any{"Id": 2, "Title": "Exam moved"})
	out, err = a.toolUpdates(ctx, map[string]string{"course": "calc"})
	if err != nil || !strings.Contains(out, "announcement: Exam moved") || strings.Contains(out, "Welcome") || strings.Contains(out, "First look") {
		t.Fatalf("second %q %v", out, err)
	}
}

func TestDiffSnapshot(t *testing.T) {
	old := snapshot{News: map[string]string{}, Grades: map[string]string{"Quiz": "|x"}, Topics: map[string]string{"1": "Notes|a"}, Folders: map[string]string{"10": "HW|2026-11-01T00:00:00.000Z"}}
	cur := snapshot{News: map[string]string{}, Grades: map[string]string{"Quiz": "80%|y"}, Topics: map[string]string{"1": "Notes|b", "2": "Slides|a"}, Folders: map[string]string{"10": "HW|2026-11-08T00:00:00.000Z"}}
	got := strings.Join(diffSnapshot(old, cur), "\n")
	for _, want := range []string{"grade: Quiz 80% (was none)", "updated content: Notes", "new content: Slides", "due date changed: HW"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
