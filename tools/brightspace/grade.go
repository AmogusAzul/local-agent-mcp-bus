package main

import (
	"context"
	"fmt"
	"strings"
)

// gradePlan is one grade value write.
type gradePlan struct {
	m        member
	key      string
	oldState string
	oldText  string
	newText  string
	body     map[string]any
}

func gradeState(g *staffGrade) string {
	if g == nil {
		return "none"
	}
	return g.LastModified + "|" + num(g.PointsNumerator)
}

func (a *api) currentGrade(ctx context.Context, org, item int64, uid string) (*staffGrade, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/grades/%d/values/%s", org, item, uid))
	if err != nil {
		return nil, err
	}
	var g staffGrade
	if err := a.getJSON(ctx, path, &g); err != nil {
		if ce, ok := err.(*codeErr); ok && ce.code == "NOT_FOUND" {
			return nil, nil
		}
		return nil, err
	}
	if g.PointsNumerator == nil && g.DisplayedGrade == "" && richText(&g.Comments) == "" {
		return nil, nil
	}
	return &g, nil
}

// keepRich re-sends a comment as the site holds it.
func keepRich(t struct{ Text, Html string }) map[string]string {
	h := t.Html
	if h == "" && t.Text != "" {
		h = mdToHTML(t.Text)
	}
	return map[string]string{"Content": h, "Type": "Html"}
}

func (a *api) toolGrade(ctx context.Context, args map[string]string) (string, error) {
	c, err := a.oneStaffCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	items, err := a.gradeItems(ctx, c.ID)
	if err != nil {
		return "", err
	}
	it, err := findGradeItem(items, args["item"])
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(it.GradeType, "Numeric") {
		return "", fail("UNSUPPORTED_FOLDER", "set it on the site, or with brightspace_api.", "%q is a %s item; this tool sets numeric grades", it.Name, it.GradeType)
	}
	var rows []map[string]string
	if from := strings.TrimSpace(args["from"]); from != "" {
		if args["who"] != "" || args["value"] != "" || args["comment"] != "" || args["private"] != "" {
			return "", invalid("pass either from or who/value/comment/private")
		}
		if rows, err = batchRows(from); err != nil {
			return "", err
		}
	} else {
		if strings.TrimSpace(args["who"]) == "" {
			return "", invalid("pass who (or from)")
		}
		rows = []map[string]string{{"who": args["who"], "value": args["value"], "comment": args["comment"], "private": args["private"]}}
	}
	ms, err := a.classlist(ctx, c.ID)
	if err != nil {
		return "", err
	}
	var plans []gradePlan
	seen := map[string]bool{}
	for i, r := range rows {
		where := ""
		if len(rows) > 1 {
			where = fmt.Sprintf("row %d: ", i+1)
		}
		if r["value"] == "" && r["score"] != "" {
			r["value"] = r["score"]
		}
		if r["value"] == "" && r["comment"] == "" && r["private"] == "" {
			return "", invalid("%spass value, comment or private", where)
		}
		m, err := pickMember(ms, r["who"])
		if err != nil {
			return "", fmt.Errorf("%s%w", where, err)
		}
		if !m.isStudent() {
			return "", invalid("%s%s is %s, not a student", where, m.DisplayName, m.ClasslistRoleDisplayName)
		}
		if seen[m.Identifier] {
			return "", invalid("%s%s appears twice", where, m.DisplayName)
		}
		seen[m.Identifier] = true
		old, err := a.currentGrade(ctx, c.ID, it.ID, m.Identifier)
		if err != nil {
			return "", err
		}
		v, err := parseNumber(r["value"], where+"value")
		if err != nil {
			return "", err
		}
		if v == nil && old != nil {
			v = old.PointsNumerator
		}
		if v == nil {
			return "", invalid("%s%s has no grade yet: pass value", where, m.DisplayName)
		}
		if *v < 0 || (it.MaxPoints != nil && *v > *it.MaxPoints) {
			return "", invalid("%svalue %s is outside 0/%s", where, points(v), points(it.MaxPoints))
		}
		body := map[string]any{"GradeObjectType": 1, "PointsNumerator": *v}
		var oldC, oldP struct{ Text, Html string }
		if old != nil {
			oldC, oldP = old.Comments, old.PrivateComments
		}
		body["Comments"], body["PrivateComments"] = keepRich(oldC), keepRich(oldP)
		desc := []string{fmt.Sprintf("%s/%s", points(v), points(it.MaxPoints))}
		if t := strings.TrimSpace(r["comment"]); t != "" {
			body["Comments"] = richInput(t)
			desc = append(desc, "comment: "+trunc(oneLine(t), 200))
		}
		if t := strings.TrimSpace(r["private"]); t != "" {
			body["PrivateComments"] = richInput(t)
			desc = append(desc, "private: "+trunc(oneLine(t), 200))
		}
		oldText := "no grade"
		if old != nil {
			oldText = old.text()
		}
		plans = append(plans, gradePlan{m: m, key: fmt.Sprintf("grade/%d/%d/%s", c.ID, it.ID, m.Identifier),
			oldState: gradeState(old), oldText: oldText, newText: strings.Join(desc, " | "), body: body})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Grades on %s (%d) | %s | %d student(s)\n", it.Name, it.ID, c.Code, len(plans))
	if it.AssociatedTool != nil && it.AssociatedTool.ToolID != 0 {
		if n := toolName(it.AssociatedTool.ToolID); n != "" {
			fmt.Fprintf(&b, "Note: this item is linked to %s %d; grading there can overwrite these values.\n", n, it.AssociatedTool.ToolItemID)
		}
	}
	for _, p := range plans {
		fmt.Fprintf(&b, "%s (%s)\n  now: %s\n  new: %s\n", p.m.DisplayName, p.m.Identifier, p.oldText, p.newText)
	}
	if args["confirm"] != "true" {
		for _, p := range plans {
			a.remember(p.key, p.oldState)
		}
		b.WriteString("Whether students see it follows the item's release settings." + notSent)
		return b.String(), nil
	}
	written := 0
	var back strings.Builder
	for _, p := range plans {
		old, err := a.currentGrade(ctx, c.ID, it.ID, p.m.Identifier)
		if err != nil {
			return writtenSoFar(written, len(plans), err), nil
		}
		if err := a.checkSeen(p.key, gradeState(old), p.m.DisplayName); err != nil {
			return writtenSoFar(written, len(plans), err), nil
		}
		path, err := a.lePath(ctx, fmt.Sprintf("/%d/grades/%d/values/%s", c.ID, it.ID, p.m.Identifier))
		if err != nil {
			return "", err
		}
		if _, err := a.sendJSON(ctx, "PUT", path, p.body, "The grade for "+p.m.DisplayName); err != nil {
			return writtenSoFar(written, len(plans), err), nil
		}
		written++
		now, err := a.currentGrade(ctx, c.ID, it.ID, p.m.Identifier)
		if err == nil {
			a.remember(p.key, gradeState(now))
			txt := "no grade"
			if now != nil {
				txt = now.text()
			}
			fmt.Fprintf(&back, "%s: %s\n", p.m.DisplayName, txt)
		}
	}
	return fmt.Sprintf("Wrote %d grade(s) on %s. The site now has:\n%s", written, it.Name, strings.TrimSpace(back.String())), nil
}

// pickMember finds one class-list member by id, code, username or name fragment.
func pickMember(ms []member, arg string) (member, error) {
	arg = strings.TrimSpace(arg)
	low := strings.ToLower(arg)
	var m []member
	for _, x := range ms {
		if x.Identifier == arg || (x.OrgDefinedID != "" && x.OrgDefinedID == arg) || strings.EqualFold(x.Username, arg) {
			return x, nil
		}
		if strings.ToLower(x.DisplayName) == low {
			return x, nil
		}
		if strings.Contains(strings.ToLower(x.DisplayName), low) {
			m = append(m, x)
		}
	}
	switch len(m) {
	case 0:
		return member{}, fail("NOT_FOUND", "list people with brightspace_people.", "Nobody in this course matches %q", arg)
	case 1:
		return m[0], nil
	}
	var ns []string
	for i, x := range m {
		if i == 6 {
			ns = append(ns, "…")
			break
		}
		ns = append(ns, fmt.Sprintf("%s %s", x.Identifier, x.DisplayName))
	}
	return member{}, invalid("%q matches several people (%s). Use the id", arg, strings.Join(ns, "; "))
}
