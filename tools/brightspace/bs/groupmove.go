package bs

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func findGroup(gs []group, arg string) (group, error) {
	arg = strings.TrimSpace(arg)
	low := strings.ToLower(arg)
	var m []group
	for _, g := range gs {
		if strconv.FormatInt(g.GroupID, 10) == arg || strings.ToLower(g.Name) == low {
			return g, nil
		}
		if strings.Contains(strings.ToLower(g.Name), low) {
			m = append(m, g)
		}
	}
	switch len(m) {
	case 0:
		return group{}, fail("NOT_FOUND", "list groups with brightspace_groups.", "No group matches %q", arg)
	case 1:
		return m[0], nil
	}
	var ns []string
	for _, g := range m {
		ns = append(ns, fmt.Sprintf("%d %s", g.GroupID, g.Name))
	}
	return group{}, invalid("%q matches several groups (%s). Use the id", arg, strings.Join(ns, "; "))
}

func hasMember(g group, uid int64) bool {
	for _, u := range g.Enrollments {
		if u == uid {
			return true
		}
	}
	return false
}

func (a *api) toolGroupMove(ctx context.Context, args map[string]string) (string, error) {
	toArg, fromArg := strings.TrimSpace(args["to"]), strings.TrimSpace(args["from_group"])
	if toArg == "" && fromArg == "" {
		return "", invalid("pass to (add), from_group (remove), or both (move)")
	}
	c, err := a.oneStaffCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	cats, err := a.groupCategories(ctx, c.ID)
	if err != nil {
		return "", err
	}
	catArg := strings.ToLower(strings.TrimSpace(args["category"]))
	var cat *groupCategory
	for i, x := range cats {
		if strconv.FormatInt(x.ID, 10) == catArg || strings.ToLower(x.Name) == catArg {
			cat = &cats[i]
			break
		}
		if strings.Contains(strings.ToLower(x.Name), catArg) {
			if cat != nil {
				return "", invalid("%q matches several group categories. Use the id", args["category"])
			}
			cat = &cats[i]
		}
	}
	if cat == nil {
		return "", fail("NOT_FOUND", "list them with brightspace_groups.", "No group category matches %q", args["category"])
	}
	m, err := a.student(ctx, c.ID, args["who"])
	if err != nil {
		return "", err
	}
	uid := m.id()
	gs, err := a.groups(ctx, c.ID, cat.ID)
	if err != nil {
		return "", err
	}
	var cur []string
	for _, g := range gs {
		if hasMember(g, uid) {
			cur = append(cur, g.Name)
		}
	}
	var to, from *group
	if toArg != "" {
		g, err := findGroup(gs, toArg)
		if err != nil {
			return "", err
		}
		if hasMember(g, uid) {
			return "", invalid("%s is already in %s", m.DisplayName, g.Name)
		}
		if cat.MaxUsersPerGroup != nil && *cat.MaxUsersPerGroup > 0 && len(g.Enrollments) >= *cat.MaxUsersPerGroup {
			return "", fail("CONFLICT", "pick another group, or raise the limit on the site.", "%s is full (%d/%d)", g.Name, len(g.Enrollments), *cat.MaxUsersPerGroup)
		}
		to = &g
	}
	if fromArg != "" {
		g, err := findGroup(gs, fromArg)
		if err != nil {
			return "", err
		}
		if !hasMember(g, uid) {
			return "", invalid("%s isn't in %s", m.DisplayName, g.Name)
		}
		from = &g
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Groups %q in %s\nStudent: %s (%s), now in: %s\n", cat.Name, c.Code, m.DisplayName, m.Identifier, orNone(strings.Join(cur, ", ")))
	if from != nil {
		fmt.Fprintf(&b, "Remove from: %s\n", from.Name)
	}
	if to != nil {
		fmt.Fprintf(&b, "Add to: %s (%d members now)\n", to.Name, len(to.Enrollments))
		if from == nil && len(cur) > 0 {
			b.WriteString("They stay in their current group too; pass from_group to move them.\n")
		}
	}
	if args["confirm"] != "true" {
		b.WriteString(strings.TrimPrefix(notSent, "\n"))
		return b.String(), nil
	}
	if err := a.approved(ctx, b.String()); err != nil {
		return "", err
	}
	base := fmt.Sprintf("/%d/groupcategories/%d/groups/", c.ID, cat.ID)
	if from != nil {
		path, err := a.lpPath(ctx, fmt.Sprintf("%s%d/enrollments/%d", base, from.GroupID, uid))
		if err != nil {
			return "", err
		}
		resp, err := a.send(ctx, func(string) (*http.Request, error) { return http.NewRequest("DELETE", a.site+path, nil) })
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return "", statusErr(resp, "Removing from "+from.Name)
		}
	}
	if to != nil {
		path, err := a.lpPath(ctx, fmt.Sprintf("%s%d/enrollments/", base, to.GroupID))
		if err != nil {
			return "", err
		}
		if _, err := a.sendJSON(ctx, "POST", path, map[string]int64{"UserId": uid}, "Adding to "+to.Name); err != nil {
			if from != nil {
				return "", fmt.Errorf("removed from %s, but %w", from.Name, err)
			}
			return "", err
		}
	}
	after, err := a.groups(ctx, c.ID, cat.ID)
	if err != nil {
		return "Saved; the read-back failed: " + err.Error(), nil
	}
	var now []string
	for _, g := range after {
		if hasMember(g, uid) {
			now = append(now, g.Name)
		}
	}
	return fmt.Sprintf("Saved. %s is now in: %s", m.DisplayName, orNone(strings.Join(now, ", "))), nil
}
