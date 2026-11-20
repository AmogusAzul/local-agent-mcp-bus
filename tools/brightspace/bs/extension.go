package bs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type specialAccess struct {
	StartDate, EndDate, DueDate *string
}

func (s *specialAccess) text() string {
	if s == nil {
		return "none"
	}
	var parts []string
	if s.DueDate != nil {
		parts = append(parts, "due "+when(parseTime(*s.DueDate)))
	}
	if s.EndDate != nil {
		parts = append(parts, "closes "+when(parseTime(*s.EndDate)))
	}
	if s.StartDate != nil {
		parts = append(parts, "opens "+when(parseTime(*s.StartDate)))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func (s *specialAccess) state() string {
	if s == nil {
		return "none"
	}
	v := func(p *string) string {
		if p == nil {
			return "-"
		}
		return *p
	}
	return v(s.StartDate) + "|" + v(s.EndDate) + "|" + v(s.DueDate)
}

// userAccess is a user's special access on a folder, or nil.
func (a *api) userAccess(ctx context.Context, org, folderID int64, uid string) (*specialAccess, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/specialaccess/", org, folderID))
	if err != nil {
		return nil, err
	}
	type row struct {
		UserID        int64 `json:"UserId"`
		SpecialAccess specialAccess
	}
	rows, err := paged[row](ctx, a, path, 20)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if strconv.FormatInt(r.UserID, 10) == uid {
			sa := r.SpecialAccess
			return &sa, nil
		}
	}
	return nil, nil
}

func (a *api) toolExtension(ctx context.Context, args map[string]string) (string, error) {
	if strings.TrimSpace(args["due"]) == "" && strings.TrimSpace(args["end"]) == "" {
		return "", invalid("pass due, end or both")
	}
	c, f, err := a.staffFolder(ctx, args["course"], args["assignment"])
	if err != nil {
		return "", err
	}
	m, err := a.student(ctx, c.ID, args["who"])
	if err != nil {
		return "", err
	}
	if !m.isStudent() {
		return "", invalid("%s is %s, not a student", m.DisplayName, m.ClasslistRoleDisplayName)
	}
	old, err := a.userAccess(ctx, c.ID, f.ID, m.Identifier)
	if err != nil {
		return "", err
	}
	next := specialAccess{}
	if old != nil {
		next = *old
	}
	for _, x := range []struct {
		arg string
		dst **string
	}{{args["due"], &next.DueDate}, {args["end"], &next.EndDate}} {
		if s := strings.TrimSpace(x.arg); s != "" {
			t, err := userTime(s)
			if err != nil {
				return "", err
			}
			v := apiTime(t)
			*x.dst = &v
		}
	}
	if next.DueDate != nil && next.EndDate != nil && parseTime(*next.EndDate).Before(parseTime(*next.DueDate)) {
		return "", invalid("end (%s) is before due (%s)", when(parseTime(*next.EndDate)), when(parseTime(*next.DueDate)))
	}
	if next.EndDate == nil && f.Availability != nil && f.Availability.EndDate != "" && next.DueDate != nil &&
		parseTime(f.Availability.EndDate).Before(parseTime(*next.DueDate)) {
		return "", invalid("the folder closes for everyone on %s, before the new due date: pass end too", when(parseTime(f.Availability.EndDate)))
	}
	key := fmt.Sprintf("sa/%d/%d/%s", c.ID, f.ID, m.Identifier)

	var b strings.Builder
	fmt.Fprintf(&b, "Special access on %s (%d) | %s\nStudent: %s (%s)\n", f.Name, f.ID, c.Code, m.DisplayName, m.Identifier)
	fmt.Fprintf(&b, "Everyone: due %s", when(parseTime(f.DueDate)))
	if f.Availability != nil && f.Availability.EndDate != "" {
		fmt.Fprintf(&b, ", closes %s", when(parseTime(f.Availability.EndDate)))
	}
	fmt.Fprintf(&b, "\nNow: %s\nNew: %s\n", old.text(), next.text())
	if isGroupFolder(f) {
		b.WriteString("Group assignment: this applies to this student only, not their group.\n")
	}
	if args["confirm"] != "true" {
		a.remember(key, old.state())
		b.WriteString(strings.TrimPrefix(notSent, "\n"))
		return b.String(), nil
	}
	if err := a.approved(ctx, b.String()); err != nil {
		return "", err
	}
	if err := a.checkSeen(key, old.state(), m.DisplayName+"'s special access"); err != nil {
		return "", err
	}
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/specialaccess/%s", c.ID, f.ID, m.Identifier))
	if err != nil {
		return "", err
	}
	if _, err := a.sendJSON(ctx, "PUT", path, next, "The special access"); err != nil {
		return "", err
	}
	now, err := a.userAccess(ctx, c.ID, f.ID, m.Identifier)
	if err != nil {
		return "Saved; the read-back failed: " + err.Error(), nil
	}
	a.remember(key, now.state())
	return fmt.Sprintf("Saved. %s on %s now has: %s", m.DisplayName, f.Name, now.text()), nil
}
