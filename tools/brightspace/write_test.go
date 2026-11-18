package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedbackPreviewThenDraft(t *testing.T) {
	a, _, st := newStaffAPI(t)
	ctx := context.Background()
	args := map[string]string{"assignment": "taller 2", "who": "T2-G 1", "score": "4.5", "text": "**Bien**: punto 2 incompleto"}
	out, err := a.toolFeedback(ctx, args)
	if err != nil || !strings.Contains(out, "now: 4/5 draft | Good start") || !strings.Contains(out, "new: 4.5/5 draft") || !strings.Contains(out, "Nothing was sent") {
		t.Fatalf("preview %q %v", out, err)
	}
	if len(st.writes) != 0 {
		t.Fatal("preview wrote")
	}
	args["confirm"] = "true"
	out, err = a.toolFeedback(ctx, args)
	if err != nil || len(st.writes) != 1 {
		t.Fatalf("confirm %q %v %v", out, err, st.writes)
	}
	w := st.writes[0]
	for _, want := range []string{"POST /d2l/api/le/1.2/5/dropbox/folders/50/feedback/group/90", `"IsGraded":false`, `"Score":4.5`, `\u003cstrong\u003eBien`} {
		if !strings.Contains(w, want) {
			t.Errorf("write %q lacks %q", w, want)
		}
	}
	if !strings.Contains(out, "T2-G 1: 4.5/5 draft") {
		t.Fatalf("read-back %q", out)
	}
}

func TestFeedbackKeepsAndValidates(t *testing.T) {
	a, _, st := newStaffAPI(t)
	ctx := context.Background()
	// Score only keeps the text; publish=true publishes.
	if _, err := a.toolFeedback(ctx, map[string]string{"assignment": "50", "who": "90", "score": "5", "publish": "true", "confirm": "true"}); err != nil {
		t.Fatal(err)
	}
	if w := st.writes[0]; !strings.Contains(w, "Good start") || !strings.Contains(w, `"IsGraded":true`) {
		t.Fatalf("write %q", w)
	}
	// Once published, a later edit without publish keeps it published.
	a.toolFeedback(ctx, map[string]string{"assignment": "50", "who": "90", "text": "x", "confirm": "true"})
	if w := st.writes[1]; !strings.Contains(w, `"IsGraded":true`) {
		t.Fatalf("unpublished by an edit: %q", w)
	}
	for name, args := range map[string]map[string]string{
		"pseudo-entity":  {"who": "0", "score": "1"},
		"unknown name":   {"who": "SYSTEM", "score": "1"},
		"over max":       {"who": "90", "score": "6"},
		"nothing to set": {"who": "90"},
		"bad level":      {"who": "90", "rubric": "corrección=Medio"},
	} {
		args["assignment"], args["confirm"] = "50", "true"
		if _, err := a.toolFeedback(ctx, args); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(st.writes) != 2 {
		t.Fatalf("refused calls wrote: %v", st.writes)
	}
}

func TestFeedbackRubricTotal(t *testing.T) {
	a, _, st := newStaffAPI(t)
	out, err := a.toolFeedback(context.Background(), map[string]string{"assignment": "50", "who": "caro", "rubric": "Corrección=Alto, present=bajo", "confirm": "true"})
	if err != nil {
		t.Fatal(err)
	}
	w := st.writes[0]
	if !strings.Contains(w, "feedback/group/91") || !strings.Contains(w, `"Score":3`) || !strings.Contains(w, `"LevelId":1`) || !strings.Contains(w, `"CriterionId":10`) {
		t.Fatalf("write %q", w)
	}
	// The fake site drops rubric assessments, as the API may: say so.
	if !strings.Contains(out, "rubric levels weren't kept") {
		t.Fatalf("read-back %q", out)
	}
}

func TestFeedbackConflict(t *testing.T) {
	a, _, st := newStaffAPI(t)
	ctx := context.Background()
	args := map[string]string{"assignment": "50", "who": "90", "score": "3"}
	if _, err := a.toolFeedback(ctx, args); err != nil {
		t.Fatal(err)
	}
	st.feedback["group/90"]["Score"] = 2.0 // someone else graded meanwhile
	args["confirm"] = "true"
	out, err := a.toolFeedback(ctx, args)
	if err != nil || !strings.Contains(out, "CONFLICT") || len(st.writes) != 0 {
		t.Fatalf("%q %v %v", out, err, st.writes)
	}
}

func TestFeedbackBatchStopsAtFirstError(t *testing.T) {
	a, _, st := newStaffAPI(t)
	ctx := context.Background()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "grades.csv")
	os.WriteFile(csvPath, []byte("who,score,feedback\nT2-G 1,4,\"bien, muy bien\"\nT2-G 2,3,regular\n"), 0o644)
	out, err := a.toolFeedback(ctx, map[string]string{"assignment": "50", "from": csvPath})
	if err != nil || !strings.Contains(out, "2 target(s)") || !strings.Contains(out, "bien, muy bien") || len(st.writes) != 0 {
		t.Fatalf("preview %q %v", out, err)
	}
	// The second write fails on the site: the first stays, and the reply says so.
	st.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/feedback/group/91") {
			http.Error(w, "boom", 500)
			return true
		}
		return false
	}
	out, err = a.toolFeedback(ctx, map[string]string{"assignment": "50", "from": csvPath, "confirm": "true"})
	if err != nil || !strings.Contains(out, "Stopped after 1 of 2") || len(st.writes) != 1 {
		t.Fatalf("batch %q %v %v", out, err, st.writes)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`[{"score":"1"}]`), 0o644)
	if _, err := a.toolFeedback(ctx, map[string]string{"assignment": "50", "from": bad}); code(err) != "INVALID_ARG" {
		t.Fatalf("row without who: %v", err)
	}
	var big strings.Builder
	big.WriteString("who,score\n")
	for i := 0; i < 201; i++ {
		big.WriteString("90,1\n")
	}
	bad = filepath.Join(dir, "big.csv")
	os.WriteFile(bad, []byte(big.String()), 0o644)
	if _, err := batchRows(bad); err == nil || !strings.Contains(err.Error(), "at most 200") {
		t.Fatalf("cap: %v", err)
	}
}

func TestPublishAll(t *testing.T) {
	a, _, st := newStaffAPI(t)
	ctx := context.Background()
	out, err := a.toolFeedback(ctx, map[string]string{"assignment": "50", "publish_all": "true"})
	if err != nil || !strings.Contains(out, "Publish 1 draft") || len(st.writes) != 0 {
		t.Fatalf("preview %q %v", out, err)
	}
	out, err = a.toolFeedback(ctx, map[string]string{"assignment": "50", "publish_all": "true", "confirm": "true"})
	if err != nil || len(st.writes) != 1 || !strings.Contains(st.writes[0], `"IsGraded":true`) || !strings.Contains(st.writes[0], `"Content":"\u003cp\u003eGood start`) {
		t.Fatalf("publish %q %v %v", out, err, st.writes)
	}
}

func TestMarkdown(t *testing.T) {
	got := mdToHTML("Hola **todos**\nsegunda línea\n\n- uno\n- [dos](https://x.co)\n\n# Título\n\n<b>x</b> *it*")
	want := `<p>Hola <strong>todos</strong><br>segunda línea</p><ul><li>uno</li><li><a href="https://x.co">dos</a></li></ul><p><strong>Título</strong></p><p>&lt;b&gt;x&lt;/b&gt; <em>it</em></p>`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestGradePreviewConfirmConflict(t *testing.T) {
	a, _, st := newStaffAPI(t)
	ctx := context.Background()
	args := map[string]string{"course": "isis", "item": "taller 2", "who": "ana", "value": "4.5", "private": "revisar"}
	out, err := a.toolGrade(ctx, args)
	if err != nil || !strings.Contains(out, "now: 4/5 (80%) | comment: ok") || !strings.Contains(out, "new: 4.5/5 | private: revisar") ||
		!strings.Contains(out, "linked to assignment 50") || len(st.writes) != 0 {
		t.Fatalf("preview %q %v", out, err)
	}
	args["confirm"] = "true"
	out, err = a.toolGrade(ctx, args)
	if err != nil || len(st.writes) != 1 {
		t.Fatalf("confirm %q %v", out, err)
	}
	w := st.writes[0]
	for _, want := range []string{"PUT /d2l/api/le/1.2/5/grades/300/values/20", `"PointsNumerator":4.5`, `"GradeObjectType":1`, `\u003cp\u003eok\u003c/p\u003e`, "revisar"} {
		if !strings.Contains(w, want) {
			t.Errorf("write %q lacks %q", w, want)
		}
	}
	// Changed on the site after a new preview: CONFLICT, nothing written.
	delete(args, "confirm")
	a.toolGrade(ctx, args)
	st.values["300/20"]["LastModified"] = "2030-01-01T00:00:00.000Z"
	args["confirm"] = "true"
	out, _ = a.toolGrade(ctx, args)
	if !strings.Contains(out, "CONFLICT") || len(st.writes) != 1 {
		t.Fatalf("conflict %q %v", out, st.writes)
	}
	for name, bad := range map[string]map[string]string{
		"over max":    {"who": "ana", "value": "6"},
		"staff":       {"who": "Me Myself", "value": "1"},
		"no value":    {"who": "beto", "comment": "hola"},
		"not numeric": {"who": "ana", "value": "1", "item": "pass"},
	} {
		bad["course"], bad["confirm"] = "isis", "true"
		if bad["item"] == "" {
			bad["item"] = "300"
		}
		if _, err := a.toolGrade(ctx, bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(st.writes) != 1 {
		t.Fatalf("refused calls wrote %v", st.writes)
	}
}

func TestAnnounce(t *testing.T) {
	a, _, st := newStaffAPI(t)
	ctx := context.Background()
	args := map[string]string{"course": "isis", "title": "Horario", "body": "Hoy **11 a 1**\n\n- sala ML 101", "start": "2030-01-02 08:00"}
	out, err := a.toolAnnounce(ctx, args)
	if err != nil || !strings.Contains(out, "Shows from:") || !strings.Contains(out, "sala ML 101") || len(st.writes) != 0 {
		t.Fatalf("preview %q %v", out, err)
	}
	if _, err := a.toolAnnounce(ctx, map[string]string{"course": "calc", "title": "x", "body": "y"}); code(err) != "NOT_STAFF" {
		t.Fatalf("student course: %v", err)
	}
	args["confirm"] = "true"
	out, err = a.toolAnnounce(ctx, args)
	if err != nil || !strings.Contains(out, "news item 77") || len(st.writes) != 1 {
		t.Fatalf("post %q %v %v", out, err, st.writes)
	}
	w := st.writes[0]
	for _, want := range []string{`"Title":"Horario"`, `strong\u003e11 a 1`, `"StartDate":"2030-01-02T`, `"IsPublished":true`} {
		if !strings.Contains(w, want) {
			t.Errorf("write %q lacks %q", w, want)
		}
	}
	f := filepath.Join(t.TempDir(), "horario.pdf")
	os.WriteFile(f, []byte("pdf"), 0o644)
	args["files"] = f
	if _, err := a.toolAnnounce(ctx, args); err != nil || st.writes[len(st.writes)-1] != "POST news multipart" {
		t.Fatalf("attachment %v %v", err, st.writes)
	}
}
