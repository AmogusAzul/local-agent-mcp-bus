package bs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// rubricChoice is one criterion=level pair resolved against a rubric.
type rubricChoice struct {
	criterionID, levelID int64
	criterion, level     string
	points               *float64
}

// parseRubric resolves "Criterion=Level, …" (names, fragments or ids)
// against the folder's rubric.
func parseRubric(r rubric, arg string) ([]rubricChoice, error) {
	sep := ","
	if strings.Contains(arg, ";") {
		sep = ";"
	}
	var out []rubricChoice
	for _, pair := range strings.Split(arg, sep) {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, invalid("rubric takes criterion=level pairs, got %q", pair)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.ToLower(strings.TrimSpace(v))
		var found []rubricChoice
		for _, g := range r.CriteriaGroups {
			for _, c := range g.Criteria {
				if strconv.FormatInt(c.ID, 10) != k && !strings.Contains(strings.ToLower(c.Name), k) {
					continue
				}
				ch := rubricChoice{criterionID: c.ID, criterion: c.Name}
				for _, l := range g.Levels {
					if strconv.FormatInt(l.ID, 10) == v || strings.ToLower(l.Name) == v {
						ch.levelID, ch.level, ch.points = l.ID, l.Name, l.Points
						for _, cell := range c.Cells {
							if cell.LevelID == l.ID && cell.Points != nil {
								ch.points = cell.Points
							}
						}
					}
				}
				if ch.levelID == 0 {
					var names []string
					for _, l := range g.Levels {
						names = append(names, l.Name)
					}
					return nil, invalid("criterion %q has no level %q (levels: %s)", c.Name, v, strings.Join(names, ", "))
				}
				found = append(found, ch)
			}
		}
		switch len(found) {
		case 0:
			return nil, invalid("rubric %q has no criterion %q", r.Name, k)
		case 1:
			out = append(out, found[0])
		default:
			var names []string
			for _, f := range found {
				names = append(names, f.criterion)
			}
			return nil, invalid("%q matches several criteria (%s)", k, strings.Join(names, "; "))
		}
	}
	return out, nil
}

// fbPlan is one feedback write: who, what is there now, what will be sent.
type fbPlan struct {
	t        target
	key      string
	oldState string
	oldText  string
	newText  string
	body     map[string]any
	rubric   bool // the write sets rubric levels
}

func fbState(fb *feedback) string {
	if fb == nil {
		return "none"
	}
	score := "-"
	if fb.Score != nil {
		score = points(fb.Score)
	}
	return fmt.Sprintf("%s|%v|%s|%d", score, fb.IsGraded, shortHash(richText(fb.Feedback)), len(fb.RubricAssessments))
}

func fbText(fb *feedback, den string) string {
	if fb == nil || (fb.Score == nil && richText(fb.Feedback) == "" && len(fb.RubricAssessments) == 0) {
		return "no feedback"
	}
	state := "draft"
	if fb.IsGraded {
		state = "published"
	}
	s := fmt.Sprintf("%s%s %s", points(fb.Score), den, state)
	if t := richText(fb.Feedback); t != "" {
		s += " | " + trunc(oneLine(t), 200)
	}
	return s
}

func entityKind(t target) string {
	if t.isGroup {
		return "group"
	}
	return "user"
}

// feedbackPath is where one user's or group's feedback is read and written.
func (a *api) feedbackPath(ctx context.Context, org, folderID int64, t target) (string, error) {
	return a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/feedback/%s/%d", org, folderID, entityKind(t), t.id))
}

func (a *api) toolFeedback(ctx context.Context, args map[string]string) (string, error) {
	c, base, err := a.staffFolder(ctx, args["course"], args["assignment"])
	if err != nil {
		return "", err
	}
	if args["publish_all"] == "true" {
		return a.publishAll(ctx, c, base, args["confirm"] == "true")
	}
	var rows []map[string]string
	if from := strings.TrimSpace(args["from"]); from != "" {
		if args["who"] != "" || args["score"] != "" || args["text"] != "" || args["rubric"] != "" {
			return "", invalid("pass either from or who/score/text/rubric")
		}
		if rows, err = batchRows(from); err != nil {
			return "", err
		}
	} else {
		if strings.TrimSpace(args["who"]) == "" {
			return "", invalid("pass who (or from, or publish_all=true)")
		}
		if args["score"] == "" && args["text"] == "" && args["rubric"] == "" && args["publish"] != "true" {
			return "", invalid("pass score, text, rubric or publish=true")
		}
		rows = []map[string]string{{"who": args["who"], "score": args["score"], "text": args["text"], "rubric": args["rubric"], "publish": args["publish"]}}
	}
	f, err := a.folder(ctx, c.ID, base.ID)
	if err != nil {
		return "", err
	}
	ts, names, err := a.folderTargets(ctx, c.ID, base)
	if err != nil {
		return "", err
	}
	es, err := a.allEntries(ctx, c.ID, base.ID)
	if err != nil {
		return "", err
	}
	byID := map[int64]*entry{}
	for i := range es {
		byID[es[i].Entity.EntityID] = &es[i]
	}
	den, max := "", (*float64)(nil)
	if f.Assessment != nil && f.Assessment.ScoreDenominator != nil {
		den, max = "/"+points(f.Assessment.ScoreDenominator), f.Assessment.ScoreDenominator
	}

	var plans []fbPlan
	seenTarget := map[int64]bool{}
	for i, r := range rows {
		where := ""
		if len(rows) > 1 {
			where = fmt.Sprintf("row %d: ", i+1)
		}
		t, err := oneTarget(ts, names, r["who"])
		if err != nil {
			return "", fmt.Errorf("%s%w", where, err)
		}
		if seenTarget[t.id] {
			return "", invalid("%s%s appears twice", where, t.name)
		}
		seenTarget[t.id] = true
		var old *feedback
		if e := byID[t.id]; e != nil {
			old = e.Feedback
		}
		score, err := parseNumber(r["score"], where+"score")
		if err != nil {
			return "", err
		}
		var choices []rubricChoice
		var rb *rubric
		if strings.TrimSpace(r["rubric"]) != "" {
			if f.Assessment == nil || len(f.Assessment.Rubrics) != 1 {
				return "", invalid("%sthis assignment has no single rubric to fill", where)
			}
			rb = &f.Assessment.Rubrics[0]
			if choices, err = parseRubric(*rb, r["rubric"]); err != nil {
				return "", fmt.Errorf("%s%w", where, err)
			}
			if score == nil {
				total := 0.0
				for _, ch := range choices {
					if ch.points != nil {
						total += *ch.points
					}
				}
				score = &total
			}
		}
		if score == nil && old != nil {
			score = old.Score
		}
		if score != nil && (*score < 0 || (max != nil && *score > *max)) {
			return "", invalid("%sscore %s is outside 0%s", where, points(score), den)
		}
		publish := old != nil && old.IsGraded
		switch r["publish"] {
		case "true":
			publish = true
		case "false":
			publish = false
		}
		body := map[string]any{"Score": score, "IsGraded": publish, "GradedSymbol": nil, "RubricAssessments": []any{}}
		newText := ""
		switch {
		case strings.TrimSpace(r["text"]) != "":
			body["Feedback"] = richInput(r["text"])
			newText = r["text"]
		case old != nil && old.Feedback != nil && (old.Feedback.Html != "" || old.Feedback.Text != ""):
			h := old.Feedback.Html
			if h == "" {
				h = mdToHTML(old.Feedback.Text)
			}
			body["Feedback"] = map[string]string{"Content": h, "Type": "Html"}
			newText = richText(old.Feedback)
		default:
			body["Feedback"] = nil
		}
		if rb != nil {
			var outcomes []any
			for _, ch := range choices {
				outcomes = append(outcomes, map[string]any{"CriterionId": ch.criterionID, "LevelId": ch.levelID, "Score": ch.points})
			}
			body["RubricAssessments"] = []any{map[string]any{"RubricId": rb.RubricID, "OverallScore": score, "CriteriaOutcome": outcomes}}
		}
		p := fbPlan{t: t, key: fmt.Sprintf("fb/%d/%d/%d", c.ID, base.ID, t.id), oldState: fbState(old), oldText: fbText(old, den), body: body, rubric: rb != nil}
		var desc []string
		state := "draft"
		if publish {
			state = "published"
		}
		desc = append(desc, fmt.Sprintf("%s%s %s", points(score), den, state))
		for _, ch := range choices {
			desc = append(desc, fmt.Sprintf("%s=%s (%s)", ch.criterion, ch.level, points(ch.points)))
		}
		if newText != "" {
			desc = append(desc, trunc(oneLine(newText), 300))
		}
		p.newText = strings.Join(desc, " | ")
		plans = append(plans, p)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Feedback on %s (%d) | %s | %d target(s)\n", base.Name, base.ID, c.Code, len(plans))
	for _, p := range plans {
		fmt.Fprintf(&b, "%s (%d)\n  now: %s\n  new: %s\n", p.t.name, p.t.id, p.oldText, p.newText)
	}
	if args["confirm"] != "true" {
		for _, p := range plans {
			a.remember(p.key, p.oldState)
		}
		b.WriteString("Drafts stay hidden from students until published." + notSent)
		return b.String(), nil
	}
	if err := a.approved(ctx, b.String()); err != nil {
		return "", err
	}

	written := 0
	for _, p := range plans {
		if err := a.checkSeen(p.key, p.oldState, p.t.name); err != nil {
			return writtenSoFar(written, len(plans), err), nil
		}
		path, err := a.feedbackPath(ctx, c.ID, base.ID, p.t)
		if err != nil {
			return "", err
		}
		if !p.rubric {
			// Keep any rubric assessment as the site holds it.
			if ra := a.currentRubric(ctx, path); ra != nil {
				p.body["RubricAssessments"] = ra
			}
		}
		if _, err := a.sendJSON(ctx, "POST", path, p.body, "The feedback for "+p.t.name); err != nil {
			return writtenSoFar(written, len(plans), err), nil
		}
		written++
	}
	return a.feedbackReadBack(ctx, c, base, plans, den)
}

// currentRubric is the rubric assessments a feedback holds now, as the
// site returns them, or nil.
func (a *api) currentRubric(ctx context.Context, path string) any {
	var cur map[string]any
	if a.getJSON(ctx, path, &cur) != nil {
		return nil
	}
	if ra, ok := cur["RubricAssessments"].([]any); ok && len(ra) > 0 {
		return ra
	}
	return nil
}

func writtenSoFar(done, total int, err error) string {
	return fmt.Sprintf("Stopped after %d of %d writes: %v", done, total, err)
}

// feedbackReadBack shows what the site now holds for the written targets.
func (a *api) feedbackReadBack(ctx context.Context, c course, f folder, plans []fbPlan, den string) (string, error) {
	es, err := a.allEntries(ctx, c.ID, f.ID)
	if err != nil {
		return fmt.Sprintf("Wrote %d feedback(s); the read-back failed: %v", len(plans), err), nil
	}
	byID := map[int64]*entry{}
	for i := range es {
		byID[es[i].Entity.EntityID] = &es[i]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Wrote %d feedback(s) on %s. The site now has:\n", len(plans), f.Name)
	for _, p := range plans {
		var fb *feedback
		if e := byID[p.t.id]; e != nil {
			fb = e.Feedback
		}
		a.remember(p.key, fbState(fb))
		fmt.Fprintf(&b, "%s: %s", p.t.name, fbText(fb, den))
		if p.rubric && (fb == nil || len(fb.RubricAssessments) == 0) {
			b.WriteString(" | rubric levels weren't kept by the API: set them on the site")
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String()), nil
}

// publishAll releases every draft feedback in a folder.
func (a *api) publishAll(ctx context.Context, c course, f folder, confirm bool) (string, error) {
	es, err := a.allEntries(ctx, c.ID, f.ID)
	if err != nil {
		return "", err
	}
	ts, _, err := a.folderTargets(ctx, c.ID, f)
	if err != nil {
		return "", err
	}
	known := map[int64]target{}
	for _, t := range ts {
		known[t.id] = t
	}
	var plans []fbPlan
	for _, e := range es {
		fb := e.Feedback
		t, ok := known[e.Entity.EntityID]
		if !ok || fb == nil || fb.IsGraded || (fb.Score == nil && richText(fb.Feedback) == "") {
			continue
		}
		plans = append(plans, fbPlan{t: t, key: fmt.Sprintf("fb/%d/%d/%d", c.ID, f.ID, t.id), oldState: fbState(fb), oldText: fbText(fb, "")})
	}
	if len(plans) == 0 {
		return fmt.Sprintf("%s has no draft feedback to publish.", f.Name), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Publish %d draft feedback(s) on %s (%s); students will see them:\n", len(plans), f.Name, c.Code)
	for _, p := range plans {
		fmt.Fprintf(&b, "%s (%d): %s\n", p.t.name, p.t.id, p.oldText)
	}
	if !confirm {
		for _, p := range plans {
			a.remember(p.key, p.oldState)
		}
		b.WriteString(strings.TrimPrefix(notSent, "\n"))
		return b.String(), nil
	}
	if err := a.approved(ctx, b.String()); err != nil {
		return "", err
	}
	written := 0
	for _, p := range plans {
		if err := a.checkSeen(p.key, p.oldState, p.t.name); err != nil {
			return writtenSoFar(written, len(plans), err), nil
		}
		path, err := a.feedbackPath(ctx, c.ID, f.ID, p.t)
		if err != nil {
			return "", err
		}
		// Re-send the feedback as it is, published. The read gives rich
		// text as Text/Html; the write takes Content/Type.
		var cur map[string]any
		if err := a.getJSON(ctx, path, &cur); err != nil {
			return writtenSoFar(written, len(plans), err), nil
		}
		body := map[string]any{"Score": cur["Score"], "IsGraded": true, "GradedSymbol": nil, "Feedback": nil, "RubricAssessments": cur["RubricAssessments"]}
		if body["RubricAssessments"] == nil {
			body["RubricAssessments"] = []any{}
		}
		if fbm, ok := cur["Feedback"].(map[string]any); ok {
			h, _ := fbm["Html"].(string)
			if h == "" {
				t, _ := fbm["Text"].(string)
				h = mdToHTML(t)
			}
			body["Feedback"] = map[string]string{"Content": h, "Type": "Html"}
		}
		if _, err := a.sendJSON(ctx, "POST", path, body, "Publishing "+p.t.name); err != nil {
			return writtenSoFar(written, len(plans), err), nil
		}
		written++
	}
	return a.feedbackReadBack(ctx, c, f, plans, "")
}
