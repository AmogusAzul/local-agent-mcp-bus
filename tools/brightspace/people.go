package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// member is a row of a course's class list.
type member struct {
	Identifier               string
	DisplayName              string
	Username                 string
	OrgDefinedID             string `json:"OrgDefinedId"`
	Email                    *string
	ClasslistRoleDisplayName string
}

func (m member) id() int64 {
	n, _ := strconv.ParseInt(m.Identifier, 10, 64)
	return n
}

// isStudent tells students from staff by the course role's name.
func (m member) isStudent() bool {
	r := strings.ToLower(m.ClasslistRoleDisplayName)
	return strings.Contains(r, "estudiante") || strings.Contains(r, "student") || strings.Contains(r, "learner")
}

func (a *api) classlist(ctx context.Context, org int64) ([]member, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/classlist/", org))
	if err != nil {
		return nil, err
	}
	var ms []member
	if err := a.getJSON(ctx, path, &ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// groupCategory is a set of groups in a course.
type groupCategory struct {
	ID                       int64 `json:"GroupCategoryId"`
	Name                     string
	EnrollmentStyle          int
	MaxUsersPerGroup         *int
	SelfEnrollmentStartDate  string
	SelfEnrollmentExpiryDate string
	Groups                   []int64
}

func (a *api) groupCategories(ctx context.Context, org int64) ([]groupCategory, error) {
	path, err := a.lpPath(ctx, fmt.Sprintf("/%d/groupcategories/", org))
	if err != nil {
		return nil, err
	}
	var cs []groupCategory
	if err := a.getJSON(ctx, path, &cs); err != nil {
		return nil, err
	}
	return cs, nil
}

// catGroups is a category with its groups.
type catGroups struct {
	cat    groupCategory
	groups []group
}

// allGroups reads every category's groups.
func (a *api) allGroups(ctx context.Context, org int64) ([]catGroups, error) {
	cs, err := a.groupCategories(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]catGroups, len(cs))
	for i, c := range cs {
		gs, err := a.groups(ctx, org, c.ID)
		if err != nil {
			return nil, err
		}
		out[i] = catGroups{c, gs}
	}
	return out, nil
}

func (a *api) toolPeople(ctx context.Context, args map[string]string) (string, error) {
	c, err := a.resolveCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	ms, err := a.classlist(ctx, c.ID)
	if err != nil {
		return "", err
	}
	cgs, _ := a.allGroups(ctx, c.ID)
	inGroups := map[int64][]string{}
	gfilter := strings.ToLower(strings.TrimSpace(args["group"]))
	matched := map[int64]bool{}
	for _, cg := range cgs {
		for _, g := range cg.groups {
			hit := gfilter != "" && strings.Contains(strings.ToLower(g.Name), gfilter)
			for _, u := range g.Enrollments {
				inGroups[u] = append(inGroups[u], g.Name)
				if hit {
					matched[u] = true
				}
			}
		}
	}
	role := strings.ToLower(strings.TrimSpace(args["role"]))
	contact := args["contact"] == "true"
	sort.Slice(ms, func(i, j int) bool { return ms[i].DisplayName < ms[j].DisplayName })
	var b strings.Builder
	n := 0
	for _, m := range ms {
		if role != "" && !strings.Contains(strings.ToLower(m.ClasslistRoleDisplayName), role) {
			continue
		}
		if gfilter != "" && !matched[m.id()] {
			continue
		}
		n++
		fmt.Fprintf(&b, "%s (%s) | %s", m.DisplayName, m.Identifier, m.ClasslistRoleDisplayName)
		if gs := inGroups[m.id()]; len(gs) > 0 {
			b.WriteString(" | " + strings.Join(gs, ", "))
		}
		if contact {
			email := ""
			if m.Email != nil {
				email = *m.Email
			}
			if email == "" && m.Username != "" {
				email = m.Username + " (username)"
			}
			fmt.Fprintf(&b, " | %s | %s", email, m.OrgDefinedID)
		}
		b.WriteByte('\n')
	}
	if n == 0 {
		return "Nobody matches.", nil
	}
	return fmt.Sprintf("%s: %d people\n%s", c, n, strings.TrimSpace(b.String())), nil
}

func (a *api) toolGroups(ctx context.Context, args map[string]string) (string, error) {
	c, err := a.resolveCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	cgs, err := a.allGroups(ctx, c.ID)
	if err != nil {
		return "", err
	}
	if len(cgs) == 0 {
		return "This course has no groups.", nil
	}
	uid, _ := a.userID(ctx)
	names := map[int64]string{}
	catArg := strings.ToLower(strings.TrimSpace(args["category"]))
	if catArg != "" {
		if ms, err := a.classlist(ctx, c.ID); err == nil {
			for _, m := range ms {
				names[m.id()] = m.DisplayName
			}
		}
	}
	var b strings.Builder
	shown := 0
	for _, cg := range cgs {
		if catArg != "" && strconv.FormatInt(cg.cat.ID, 10) != catArg && !strings.Contains(strings.ToLower(cg.cat.Name), catArg) {
			continue
		}
		shown++
		fmt.Fprintf(&b, "%s (category %d), %d groups", cg.cat.Name, cg.cat.ID, len(cg.groups))
		if cg.cat.MaxUsersPerGroup != nil && *cg.cat.MaxUsersPerGroup > 0 {
			fmt.Fprintf(&b, ", up to %d each", *cg.cat.MaxUsersPerGroup)
		}
		if e := parseTime(cg.cat.SelfEnrollmentExpiryDate); !e.IsZero() {
			s := parseTime(cg.cat.SelfEnrollmentStartDate)
			if s.IsZero() {
				fmt.Fprintf(&b, ", self-enrollment until %s", when(e))
			} else {
				fmt.Fprintf(&b, ", self-enrollment %s – %s", when(s), when(e))
			}
		}
		b.WriteByte('\n')
		for _, g := range cg.groups {
			fmt.Fprintf(&b, "  %s (%d): %d", g.Name, g.GroupID, len(g.Enrollments))
			if cg.cat.MaxUsersPerGroup != nil && *cg.cat.MaxUsersPerGroup > 0 {
				fmt.Fprintf(&b, "/%d", *cg.cat.MaxUsersPerGroup)
			}
			mine := false
			var who []string
			for _, u := range g.Enrollments {
				if u == uid {
					mine = true
				}
				if n := names[u]; n != "" {
					who = append(who, n)
				} else if catArg != "" {
					who = append(who, strconv.FormatInt(u, 10))
				}
			}
			if mine {
				b.WriteString(" (yours)")
			}
			if len(who) > 0 {
				b.WriteString(" — " + strings.Join(who, ", "))
			}
			b.WriteByte('\n')
		}
	}
	if shown == 0 {
		return "", fail("NOT_FOUND", "omit category to list them.", "No group category matches %q", args["category"])
	}
	return strings.TrimSpace(b.String()), nil
}

func apiTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (a *api) toolCalendar(ctx context.Context, args map[string]string) (string, error) {
	days := 14
	if v := args["days"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 180 {
			return "", invalid("days must be a number from 1 to 180")
		}
		days = n
	}
	cs, err := a.courseOrCurrent(ctx, args["course"])
	if err != nil {
		return "", err
	}
	if len(cs) == 0 {
		return "No current courses.", nil
	}
	codes := map[int64]string{}
	var ids []string
	for _, c := range cs {
		ids = append(ids, strconv.FormatInt(c.ID, 10))
		codes[c.ID] = c.Code
	}
	now := a.now()
	q := url.Values{}
	q.Set("orgUnitIdsCSV", strings.Join(ids, ","))
	q.Set("startDateTime", apiTime(now))
	q.Set("endDateTime", apiTime(now.AddDate(0, 0, days)))
	path, err := a.lePath(ctx, "/calendar/events/myEvents/?"+q.Encode())
	if err != nil {
		return "", err
	}
	type event struct {
		Title         string
		StartDateTime string
		EndDateTime   string
		IsAllDayEvent bool
		LocationName  string
		OrgUnitID     int64 `json:"OrgUnitId"`
	}
	var evs []event
	for page := 0; page < 10 && path != ""; page++ {
		var r struct {
			Objects []event
			Next    *string
		}
		if err := a.getJSON(ctx, path, &r); err != nil {
			return "", err
		}
		evs = append(evs, r.Objects...)
		path = ""
		if r.Next != nil && *r.Next != "" {
			path = strings.TrimPrefix(*r.Next, a.site)
		}
	}
	sort.SliceStable(evs, func(i, j int) bool { return parseTime(evs[i].StartDateTime).Before(parseTime(evs[j].StartDateTime)) })
	var b strings.Builder
	for i, e := range evs {
		if i == 80 {
			b.WriteString("…\n")
			break
		}
		start := parseTime(e.StartDateTime)
		if e.IsAllDayEvent {
			fmt.Fprintf(&b, "%s (all day)", start.Local().Format("Mon 2 Jan"))
		} else {
			b.WriteString(when(start))
			if end := parseTime(e.EndDateTime); end.After(start) {
				b.WriteString("–" + end.Local().Format("15:04"))
			}
		}
		fmt.Fprintf(&b, " | %s | %s", codes[e.OrgUnitID], e.Title)
		if e.LocationName != "" {
			b.WriteString(" @ " + e.LocationName)
		}
		b.WriteByte('\n')
	}
	if len(evs) == 0 {
		return fmt.Sprintf("No calendar events in the next %d days.", days), nil
	}
	return strings.TrimSpace(b.String()), nil
}

// snapshot is what brightspace_updates remembers about one course.
type snapshot struct {
	News    map[string]string `json:"news"`    // id → title
	Grades  map[string]string `json:"grades"`  // item → shown grade and comment hash
	Topics  map[string]string `json:"topics"`  // id → title | modified
	Folders map[string]string `json:"folders"` // id → name | due
}

func (a *api) snapshotPath() string {
	return filepath.Join(filepath.Dir(a.s.path), "brightspace-updates.json")
}

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:4])
}

// takeSnapshot reads one course's state. Parts the user can't read stay nil.
func (a *api) takeSnapshot(ctx context.Context, org int64) (snapshot, map[string]int, error) {
	var s snapshot
	var firstErr error
	keep := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	if path, err := a.lePath(ctx, fmt.Sprintf("/%d/news/", org)); err == nil {
		var ns []struct {
			ID    int64 `json:"Id"`
			Title string
		}
		if err := a.getJSON(ctx, path, &ns); err == nil {
			s.News = map[string]string{}
			for _, n := range ns {
				s.News[strconv.FormatInt(n.ID, 10)] = n.Title
			}
		} else {
			keep(err)
		}
	}
	if gs, err := a.grades(ctx, org); err == nil {
		s.Grades = map[string]string{}
		for _, g := range gs {
			s.Grades[g.GradeObjectName] = g.DisplayedGrade + "|" + shortHash(g.Comments.Text)
		}
	}
	if ms, err := a.toc(ctx, org); err == nil {
		var flat []flatTopic
		flatten(ms, nil, &flat)
		s.Topics = map[string]string{}
		for _, t := range flat {
			s.Topics[strconv.FormatInt(t.ID, 10)] = t.Title + "|" + t.LastModifiedDate
		}
	} else {
		keep(err)
	}
	if fs, err := a.folders(ctx, org); err == nil {
		s.Folders = map[string]string{}
		for _, f := range fs {
			if !f.IsHidden {
				s.Folders[strconv.FormatInt(f.ID, 10)] = f.Name + "|" + f.DueDate
			}
		}
	}
	var counts map[string]int
	if path, err := a.lePath(ctx, fmt.Sprintf("/%d/updates/myUpdates", org)); err == nil {
		var raw map[string]any
		if a.getJSON(ctx, path, &raw) == nil {
			counts = map[string]int{}
			for k, v := range raw {
				if f, ok := v.(float64); ok && f > 0 && k != "OrgUnitId" && k != "UserId" {
					counts[k] = int(f)
				}
			}
		}
	}
	if s.News == nil && s.Topics == nil && firstErr != nil {
		return s, counts, firstErr
	}
	return s, counts, nil
}

func before(s string) string {
	if i := strings.LastIndex(s, "|"); i >= 0 {
		return s[:i]
	}
	return s
}

func after(s string) string {
	if i := strings.LastIndex(s, "|"); i >= 0 {
		return s[i+1:]
	}
	return ""
}

// diffSnapshot lists what changed from old to cur.
func diffSnapshot(old, cur snapshot) []string {
	var out []string
	keys := func(m map[string]string) []string {
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	if old.News != nil {
		for _, k := range keys(cur.News) {
			if _, ok := old.News[k]; !ok {
				out = append(out, "announcement: "+cur.News[k])
			}
		}
	}
	if old.Grades != nil {
		for _, k := range keys(cur.Grades) {
			o, ok := old.Grades[k]
			n := cur.Grades[k]
			switch {
			case !ok && before(n) != "":
				out = append(out, fmt.Sprintf("grade: %s %s", k, before(n)))
			case ok && before(o) != before(n):
				out = append(out, fmt.Sprintf("grade: %s %s (was %s)", k, before(n), orNone(before(o))))
			case ok && after(o) != after(n):
				out = append(out, fmt.Sprintf("grade comment changed: %s", k))
			}
		}
	}
	if old.Topics != nil {
		for _, k := range keys(cur.Topics) {
			o, ok := old.Topics[k]
			switch {
			case !ok:
				out = append(out, "new content: "+before(cur.Topics[k]))
			case o != cur.Topics[k]:
				out = append(out, "updated content: "+before(cur.Topics[k]))
			}
		}
	}
	if old.Folders != nil {
		for _, k := range keys(cur.Folders) {
			o, ok := old.Folders[k]
			n := cur.Folders[k]
			due := parseTime(after(n))
			switch {
			case !ok:
				out = append(out, fmt.Sprintf("new assignment: %s (due %s)", before(n), when(due)))
			case after(o) != after(n):
				out = append(out, fmt.Sprintf("due date changed: %s now %s", before(n), when(due)))
			}
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (a *api) toolUpdates(ctx context.Context, args map[string]string) (string, error) {
	cs, err := a.courseOrCurrent(ctx, args["course"])
	if err != nil {
		return "", err
	}
	type res struct {
		s      snapshot
		counts map[string]int
		err    error
	}
	rs := eachCourse(cs, func(c course) res {
		s, counts, err := a.takeSnapshot(ctx, c.ID)
		return res{s, counts, err}
	})
	all := map[string]snapshot{}
	if b, err := os.ReadFile(a.snapshotPath()); err == nil {
		json.Unmarshal(b, &all)
	}
	var b strings.Builder
	fresh := 0
	for i, r := range rs {
		c := cs[i]
		key := strconv.FormatInt(c.ID, 10)
		if r.err != nil {
			fmt.Fprintf(&b, "%s: %v\n", c.Code, r.err)
			continue
		}
		var lines []string
		if old, ok := all[key]; ok {
			lines = diffSnapshot(old, r.s)
		} else {
			fresh++
		}
		var counts []string
		for k, v := range r.counts {
			counts = append(counts, fmt.Sprintf("%s %d", k, v))
		}
		sort.Strings(counts)
		all[key] = r.s
		if len(lines) == 0 && len(counts) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s\n", c)
		for i, l := range lines {
			if i == 25 {
				fmt.Fprintf(&b, "  … %d more\n", len(lines)-25)
				break
			}
			b.WriteString("  " + l + "\n")
		}
		if len(counts) > 0 {
			b.WriteString("  site counts: " + strings.Join(counts, ", ") + "\n")
		}
	}
	if js, err := json.Marshal(all); err == nil {
		os.MkdirAll(filepath.Dir(a.snapshotPath()), 0o700)
		os.WriteFile(a.snapshotPath(), js, 0o600)
	}
	out := strings.TrimSpace(b.String())
	if fresh > 0 {
		note := fmt.Sprintf("First look at %d course(s): their state is saved, and the next call reports changes since now.", fresh)
		if out == "" {
			return note, nil
		}
		return out + "\n" + note, nil
	}
	if out == "" {
		return "Nothing new since the last check.", nil
	}
	return out, nil
}
