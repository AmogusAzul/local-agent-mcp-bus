package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

// staffSite serves course 5, which the user teaches. writes collects every
// non-GET request as "METHOD path body".
type staffSite struct {
	writes   []string
	feedback map[string]map[string]any // "group/90" → feedback
	values   map[string]map[string]any // "300/20" → grade value
	extra    func(w http.ResponseWriter, r *http.Request) bool
}

func newStaffAPI(t *testing.T) (*api, *fakeSite, *staffSite) {
	a, site, _ := newTestAPI(t, true)
	a.staff = true
	st := &staffSite{
		feedback: map[string]map[string]any{
			"group/90": {"Score": 4.0, "Feedback": map[string]string{"Text": "Good start", "Html": "<p>Good start</p>"}, "IsGraded": false, "RubricAssessments": []any{}},
		},
		values: map[string]map[string]any{
			"300/20": {"PointsNumerator": 4.0, "PointsDenominator": 5.0, "DisplayedGrade": "80%", "Comments": map[string]string{"Text": "", "Html": "<p>ok</p>"}, "LastModified": "2026-11-01T00:00:00.000Z"},
		},
	}
	site.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if st.extra != nil && st.extra(w, r) {
			return true
		}
		return st.serve(w, r)
	}
	return a, site, st
}

func (st *staffSite) serve(w http.ResponseWriter, r *http.Request) bool {
	out := func(v any) { json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	if r.Method != "GET" && strings.Contains(p, "/5/") {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		js, _ := json.Marshal(body)
		st.writes = append(st.writes, r.Method+" "+p+" "+string(js))
		const fb = "/d2l/api/le/1.2/5/dropbox/folders/50/feedback/"
		if strings.HasPrefix(p, fb) {
			if body["Feedback"] != nil {
				fbm := body["Feedback"].(map[string]any)
				body["Feedback"] = map[string]any{"Text": plain(fbm["Content"].(string)), "Html": fbm["Content"]}
			}
			body["RubricAssessments"] = []any{} // the API may not keep rubric levels sent here
			st.feedback[strings.TrimPrefix(p, fb)] = body
		}
		if strings.HasPrefix(p, "/d2l/api/le/1.2/5/grades/300/values/") {
			body["LastModified"] = time.Now().UTC().Format(time.RFC3339)
			st.values["300/"+strings.TrimPrefix(p, "/d2l/api/le/1.2/5/grades/300/values/")] = body
		}
		w.WriteHeader(200)
		return true
	}
	switch {
	case p == "/d2l/api/le/1.2/5/dropbox/folders/":
		out([]any{
			map[string]any{"Id": 50, "Name": "Taller 2", "DueDate": past, "GroupTypeId": 9, "DropboxType": 1, "GradeItemId": 300,
				"TotalUsers": 2, "TotalUsersWithSubmissions": 1, "TotalUsersWithFeedback": 0, "UnreadFiles": 1},
			map[string]any{"Id": 51, "Name": "Hidden draft", "DueDate": past, "DropboxType": 2, "IsHidden": true},
		})
	case p == "/d2l/api/le/1.2/5/dropbox/folders/50":
		out(map[string]any{"Id": 50, "Name": "Taller 2", "DueDate": past, "GroupTypeId": 9, "DropboxType": 1,
			"Assessment": map[string]any{"ScoreDenominator": 5, "Rubrics": []any{map[string]any{"RubricId": 3, "Name": "R",
				"CriteriaGroups": []any{map[string]any{"Levels": []any{map[string]any{"Id": 1, "Name": "Alto", "Points": 2}, map[string]any{"Id": 2, "Name": "Bajo", "Points": 0}},
					"Criteria": []any{map[string]any{"Id": 9, "Name": "Corrección", "Cells": []any{map[string]any{"LevelId": 1, "Points": 3}, map[string]any{"LevelId": 2, "Points": 1}}},
						map[string]any{"Id": 10, "Name": "Presentación", "Cells": []any{map[string]any{"LevelId": 1, "Points": 2}, map[string]any{"LevelId": 2, "Points": 0}}}}}}}}}})
	case p == "/d2l/api/le/1.2/5/dropbox/folders/51/submissions/":
		out([]any{})
	case p == "/d2l/api/le/1.2/5/dropbox/folders/51":
		out(map[string]any{"Id": 51, "Name": "Hidden draft", "DropboxType": 2})
	case p == "/d2l/api/le/1.2/5/dropbox/folders/50/submissions/":
		e := map[string]any{"Entity": map[string]any{"EntityId": 90, "EntityType": "Group", "Name": "T2-G 1", "Active": true},
			"Submissions": []any{map[string]any{"Id": 1, "SubmittedBy": map[string]string{"Identifier": "20", "DisplayName": "Ana Ruiz"},
				"SubmissionDate": time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
				"Files":          []any{map[string]any{"FileId": 700, "FileName": "t2.pdf", "Size": 2048, "IsRead": false}}}}}
		if f := st.feedback["group/90"]; f != nil {
			e["Feedback"] = f
		}
		es := []any{e}
		if f := st.feedback["group/91"]; f != nil {
			es = append(es, map[string]any{"Entity": map[string]any{"EntityId": 91, "EntityType": "Group", "Name": "T2-G 2", "Active": true}, "Feedback": f})
		}
		out(es)
	case strings.HasPrefix(p, "/d2l/api/le/1.2/5/dropbox/folders/50/feedback/"):
		if f := st.feedback[strings.TrimPrefix(p, "/d2l/api/le/1.2/5/dropbox/folders/50/feedback/")]; f != nil {
			out(f)
		} else {
			http.NotFound(w, r)
		}
	case p == "/d2l/api/le/1.2/5/dropbox/folders/50/submissions/1/files/700":
		w.Write([]byte("pdf"))
	case p == "/d2l/api/lp/1.1/5/groupcategories/9/groups/":
		out([]any{map[string]any{"GroupId": 90, "Name": "T2-G 1", "Enrollments": []int{20, 21}}, map[string]any{"GroupId": 91, "Name": "T2-G 2", "Enrollments": []int{22}}})
	case p == "/d2l/api/lp/1.1/5/groupcategories/":
		out([]any{map[string]any{"GroupCategoryId": 9, "Name": "Taller 2", "MaxUsersPerGroup": 3, "Groups": []int{90, 91}}})
	case p == "/d2l/api/le/1.2/5/classlist/":
		out([]any{
			map[string]any{"Identifier": "7", "DisplayName": "Me Myself", "ClasslistRoleDisplayName": "Monitor"},
			map[string]any{"Identifier": "20", "DisplayName": "Ana Ruiz", "ClasslistRoleDisplayName": "Estudiante", "OrgDefinedId": "2020"},
			map[string]any{"Identifier": "21", "DisplayName": "Beto Paz", "ClasslistRoleDisplayName": "Estudiante"},
			map[string]any{"Identifier": "22", "DisplayName": "Caro Gil", "ClasslistRoleDisplayName": "Estudiante"},
		})
	case p == "/d2l/api/le/1.2/5/updates/myUpdates":
		out(map[string]any{"UngradedQuizzes": 3})
	case p == "/d2l/api/le/1.2/5/grades/":
		out([]any{map[string]any{"Id": 300, "Name": "Taller 2", "MaxPoints": 5, "Weight": 10, "GradeType": "Numeric", "AssociatedTool": map[string]any{"ToolId": 2000, "ToolItemId": 50}},
			map[string]any{"Id": 301, "Name": "Quiz 1", "MaxPoints": 10, "GradeType": "Numeric"}, map[string]any{"Id": 302, "Name": "Pass/fail", "GradeType": "PassFail"}})
	case p == "/d2l/api/le/1.2/5/grades/301":
		out(map[string]any{"Id": 301, "Name": "Quiz 1", "MaxPoints": 10})
	case p == "/d2l/api/le/1.2/5/grades/300/statistics":
		out(map[string]any{"Average": 80.5, "Median": 82})
	case p == "/d2l/api/le/1.2/5/grades/300/values/":
		row := func(id, name string) map[string]any {
			var gv any
			if v := st.values["300/"+id]; v != nil {
				gv = v
			}
			return map[string]any{"User": map[string]string{"Identifier": id, "DisplayName": name}, "GradeValue": gv}
		}
		if r.URL.Query().Get("page") == "" {
			next := "/d2l/api/le/1.2/5/grades/300/values/?page=2"
			out(map[string]any{"Objects": []any{row("20", "Ana Ruiz")}, "Next": next})
		} else {
			out(map[string]any{"Objects": []any{row("21", "Beto Paz")}, "Next": nil})
		}
	case strings.HasPrefix(p, "/d2l/api/le/1.2/5/grades/300/values/"):
		id := strings.TrimPrefix(p, "/d2l/api/le/1.2/5/grades/300/values/")
		if v := st.values["300/"+id]; v != nil {
			out(v)
		} else {
			http.NotFound(w, r)
		}
	case p == "/d2l/api/le/1.2/5/grades/values/20/":
		out([]any{map[string]any{"GradeObjectName": "Taller 2", "PointsNumerator": 4, "PointsDenominator": 5, "DisplayedGrade": "80%",
			"PrivateComments": map[string]string{"Text": "copied?"}}})
	case p == "/d2l/api/le/1.2/5/grades/final/values/20":
		out(map[string]any{"GradeObjectName": "Final", "DisplayedGrade": "86%"})
	case p == "/d2l/api/le/1.2/5/quizzes/":
		out(map[string]any{"Objects": []any{map[string]any{"QuizId": 400, "Name": "Quiz 1", "GradeItemId": 301}}, "Next": nil})
	case p == "/d2l/api/le/1.2/5/quizzes/400/attempts/":
		at := func(id, user, n int, score float64) map[string]any {
			return map[string]any{"AttemptId": id, "UserId": user, "AttemptNumber": n, "Score": score, "IsPublished": true,
				"Started": "2026-08-18T15:35:00.000Z", "Completed": "2026-08-18T15:39:00.000Z"}
		}
		out(map[string]any{"Objects": []any{at(1, 20, 1, 6), at(2, 20, 2, 9), at(3, 21, 1, 4)}, "Next": nil})
	default:
		return false
	}
	return true
}

func code(err error) string {
	if ce, ok := err.(*codeErr); ok {
		return ce.code
	}
	return ""
}

func TestStaffCourseGate(t *testing.T) {
	a, _, _ := newStaffAPI(t)
	ctx := context.Background()
	if _, err := a.toolStaff(ctx, map[string]string{"course": "calc"}); code(err) != "NOT_STAFF" {
		t.Fatalf("student course: %v", err)
	}
	if _, err := a.toolGradebook(ctx, map[string]string{"course": "1"}); code(err) != "NOT_STAFF" {
		t.Fatalf("student course by id: %v", err)
	}
	cs, err := a.staffCourses(ctx, "")
	if err != nil || len(cs) != 1 || cs[0].ID != 5 {
		t.Fatalf("staff courses %v %v", cs, err)
	}
	out, err := a.toolStaff(ctx, map[string]string{})
	if err != nil || !strings.Contains(out, "Taller 2 (50)") || !strings.Contains(out, "1/2 submitted, 0 with feedback, 1 to grade, 1 unread files | group") ||
		!strings.Contains(out, "UngradedQuizzes: 3") || !strings.Contains(out, "hidden") {
		t.Fatalf("staff %q %v", out, err)
	}
}

func TestStaffToolsOptIn(t *testing.T) {
	a := newAPI(newSession("https://x"))
	size := func(ts []core.Tool) int {
		n := 0
		for _, tl := range ts {
			n += len(tl.Name) + len(tl.Description)
			for _, a := range tl.Args {
				n += len(a.Name) + len(a.Description)
			}
		}
		return n
	}
	student := newTools(a)
	for _, tl := range student {
		if tl.Name == "brightspace_staff" {
			t.Fatal("staff tool without BRIGHTSPACE_STAFF")
		}
	}
	a.staff = true
	staff := newTools(a)
	if len(staff) <= len(student) {
		t.Fatal("no staff tools with staff on")
	}
	if n := size(staff); n > 8000 {
		t.Fatalf("staff tool text is %d bytes", n)
	}
	t.Logf("tool text: %d bytes student, %d with staff", size(student), size(staff))
}

func TestSubmissionsView(t *testing.T) {
	a, _, _ := newStaffAPI(t)
	ctx := context.Background()
	out, err := a.toolSubmissions(ctx, map[string]string{"assignment": "taller 2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Taller 2 (50) | 202620_ISIS_3 | group", "out of 5", "1 submitted (1 late), 1 missing, 1 with feedback (0 published, 1 draft)",
		"T2-G 1 (90)", "by Ana Ruiz", "[700] t2.pdf (2 KB, unread)", "4/5 draft", "feedback: Good start", "T2-G 2 (91) | not submitted"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	out, _ = a.toolSubmissions(ctx, map[string]string{"assignment": "50", "status": "missing"})
	if strings.Contains(out, "T2-G 1 (90)") || !strings.Contains(out, "T2-G 2") {
		t.Fatalf("missing filter %q", out)
	}
	out, _ = a.toolSubmissions(ctx, map[string]string{"assignment": "50", "who": "caro"})
	if !strings.Contains(out, "T2-G 2") || strings.Contains(out, "T2-G 1 (90)") {
		t.Fatalf("who by member %q", out)
	}
	// Staff see hidden folders.
	if _, err := a.toolSubmissions(ctx, map[string]string{"assignment": "hidden"}); err != nil {
		t.Fatalf("hidden folder: %v", err)
	}
	if _, err := a.toolSubmissions(ctx, map[string]string{"assignment": "50", "status": "weird"}); code(err) != "INVALID_ARG" {
		t.Fatal("bad status accepted")
	}
}

func TestGradebookViews(t *testing.T) {
	a, _, _ := newStaffAPI(t)
	ctx := context.Background()
	out, err := a.toolGradebook(ctx, map[string]string{"course": "isis"})
	if err != nil || !strings.Contains(out, "Taller 2 (300) | max 5 | weight 10 | assignment 50 | avg 80.5%, median 82.0%") {
		t.Fatalf("items %q %v", out, err)
	}
	out, err = a.toolGradebook(ctx, map[string]string{"course": "isis", "item": "taller"})
	if err != nil || !strings.Contains(out, "Ana Ruiz (20) | 4/5 (80%) | comment: ok") || !strings.Contains(out, "Beto Paz (21) | no grade") {
		t.Fatalf("item values (paged) %q %v", out, err)
	}
	out, err = a.toolGradebook(ctx, map[string]string{"course": "isis", "who": "ana"})
	if err != nil || !strings.Contains(out, "private: copied?") || !strings.Contains(out, "Final (Final): 86%") {
		t.Fatalf("student row %q %v", out, err)
	}
}

func TestQuizResults(t *testing.T) {
	a, _, _ := newStaffAPI(t)
	ctx := context.Background()
	out, err := a.toolQuizResults(ctx, map[string]string{"quiz": "quiz 1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Ana Ruiz (20) | best 9 | 2 attempt(s) | took 4m0s", "Beto Paz (21) | best 4", "No attempt (1): Caro Gil"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	out, _ = a.toolQuizResults(ctx, map[string]string{"quiz": "400", "who": "ana"})
	if !strings.Contains(out, "attempt 1: 6 |") || !strings.Contains(out, "attempt 2: 9 |") {
		t.Fatalf("one student %q", out)
	}
}

func TestSyncSubmissions(t *testing.T) {
	a, _, _ := newStaffAPI(t)
	dest := t.TempDir()
	out, err := a.toolSync(context.Background(), map[string]string{"assignment": "taller 2", "dest": dest})
	if err != nil || !strings.Contains(out, "1 downloaded") || !strings.Contains(out, "mark unread") {
		t.Fatalf("%q %v", out, err)
	}
	a.staff = false
	if _, err := a.toolSync(context.Background(), map[string]string{"assignment": "taller 2", "dest": dest}); code(err) != "NOT_STAFF" {
		t.Fatalf("without staff: %v", err)
	}
}
