package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func notStaff(c course) error {
	return fail("NOT_STAFF", "staff tools work only in courses where you teach (e.g. as Monitor).", "You aren't staff in %s", c)
}

// staffCourses resolves a course argument among the courses the user
// teaches. Empty means every current one. A course the user only studies in
// answers NOT_STAFF.
func (a *api) staffCourses(ctx context.Context, arg string) ([]course, error) {
	cs, err := a.allCourses(ctx)
	if err != nil {
		return nil, err
	}
	arg = strings.TrimSpace(arg)
	var staff []course
	for _, c := range cs {
		if c.Staff {
			staff = append(staff, c)
		}
	}
	if arg == "" {
		var cur []course
		for _, c := range staff {
			if c.Current {
				cur = append(cur, c)
			}
		}
		if len(cur) == 0 {
			return nil, fail("NOT_STAFF", "", "You aren't staff in any current course")
		}
		return cur, nil
	}
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, c := range cs {
			if c.ID == id {
				if !c.Staff {
					return nil, notStaff(c)
				}
				return []course{c}, nil
			}
		}
		return nil, fail("NOT_FOUND", "list courses with brightspace_courses.", "You have no course with id %d", id)
	}
	m := matchCourses(staff, arg, true)
	if len(m) == 0 {
		m = matchCourses(staff, arg, false)
	}
	if len(m) > 0 {
		return m, nil
	}
	if other := matchCourses(cs, arg, false); len(other) > 0 {
		return nil, notStaff(other[0])
	}
	return nil, fail("NOT_FOUND", "list courses with brightspace_courses.", "No course matches %q", arg)
}

// oneStaffCourse is staffCourses narrowed to exactly one course.
func (a *api) oneStaffCourse(ctx context.Context, arg string) (course, error) {
	cs, err := a.staffCourses(ctx, arg)
	if err != nil {
		return course{}, err
	}
	if len(cs) == 1 {
		return cs[0], nil
	}
	var names []string
	for _, c := range cs {
		names = append(names, fmt.Sprintf("%d %s", c.ID, c.Code))
	}
	return course{}, invalid("pick one course (%s)", strings.Join(names, "; "))
}

// staffFolder finds an assignment folder in the staff courses that match
// courseArg; with several courses the folder must be in only one of them.
func (a *api) staffFolder(ctx context.Context, courseArg, folderArg string) (course, folder, error) {
	cs, err := a.staffCourses(ctx, courseArg)
	if err != nil {
		return course{}, folder{}, err
	}
	type hit struct {
		c   course
		f   folder
		err error
	}
	hits := parallel(cs, func(c course) hit {
		fs, err := a.folders(ctx, c.ID)
		if err != nil {
			return hit{c: c, err: err}
		}
		f, err := findFolderIn(fs, folderArg, true)
		return hit{c, f, err}
	})
	if len(hits) == 1 {
		return hits[0].c, hits[0].f, hits[0].err
	}
	var found []hit
	var firstErr error
	for _, h := range hits {
		if h.err == nil {
			found = append(found, h)
		} else if firstErr == nil {
			firstErr = h.err
		}
	}
	switch len(found) {
	case 0:
		return course{}, folder{}, firstErr
	case 1:
		return found[0].c, found[0].f, nil
	}
	var names []string
	for _, h := range found {
		names = append(names, fmt.Sprintf("%s: %d %s", h.c.Code, h.f.ID, h.f.Name))
	}
	return course{}, folder{}, invalid("%q is in several courses (%s). Pass course", folderArg, strings.Join(names, "; "))
}

func (a *api) toolStaff(ctx context.Context, args map[string]string) (string, error) {
	cs, err := a.staffCourses(ctx, args["course"])
	if err != nil {
		return "", err
	}
	type res struct {
		fs     []folder
		counts map[string]int
		err    error
	}
	rs := eachCourse(cs, func(c course) res {
		fs, err := a.folders(ctx, c.ID)
		var counts map[string]int
		if p, err := a.lePath(ctx, fmt.Sprintf("/%d/updates/myUpdates", c.ID)); err == nil {
			var raw map[string]any
			if a.getJSON(ctx, p, &raw) == nil {
				counts = map[string]int{}
				for _, k := range []string{"UnreadAssignmentSubmissions", "UngradedQuizzes", "UnapprovedDiscussions", "PendingEnrollmentRequests"} {
					if f, ok := raw[k].(float64); ok && f > 0 {
						counts[k] = int(f)
					}
				}
			}
		}
		return res{fs, counts, err}
	})
	var b strings.Builder
	for i, r := range rs {
		c := cs[i]
		fmt.Fprintf(&b, "%d %s\n", c.ID, c)
		if r.err != nil {
			fmt.Fprintf(&b, "  %v\n", r.err)
			continue
		}
		for k, v := range r.counts {
			fmt.Fprintf(&b, "  %s: %d\n", k, v)
		}
		sort.SliceStable(r.fs, func(i, j int) bool { return parseTime(r.fs[i].DueDate).After(parseTime(r.fs[j].DueDate)) })
		for _, f := range r.fs {
			fmt.Fprintf(&b, "  %s (%d) | due %s | %d/%d submitted, %d with feedback", f.Name, f.ID, when(parseTime(f.DueDate)),
				f.TotalUsersWithSubmissions, f.TotalUsers, f.TotalUsersWithFeedback)
			if f.TotalUsersWithSubmissions > f.TotalUsersWithFeedback {
				fmt.Fprintf(&b, ", %d to grade", f.TotalUsersWithSubmissions-f.TotalUsersWithFeedback)
			}
			if f.UnreadFiles > 0 {
				fmt.Fprintf(&b, ", %d unread files", f.UnreadFiles)
			}
			if isGroupFolder(f) {
				b.WriteString(" | group")
			}
			if f.IsHidden {
				b.WriteString(" | hidden")
			}
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// allEntries lists every user's or group's entry in a folder (staff).
func (a *api) allEntries(ctx context.Context, org, folderID int64) ([]entry, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/submissions/?activeOnly=true", org, folderID))
	if err != nil {
		return nil, err
	}
	var es []entry
	if err := a.getJSON(ctx, path, &es); err != nil {
		return nil, err
	}
	return es, nil
}

func (e entry) name() string {
	if e.Entity.Name != "" {
		return e.Entity.Name
	}
	return e.Entity.DisplayName
}

// oneLine joins a text's non-empty lines with " / ".
func oneLine(s string) string {
	var parts []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " / ")
}

func richText(t *struct{ Text, Html string }) string {
	if t == nil {
		return ""
	}
	if s := strings.TrimSpace(t.Text); s != "" {
		return s
	}
	return plain(t.Html)
}

// target is a user or group a staff tool acts on in a folder.
type target struct {
	id      int64
	name    string
	isGroup bool
	members []int64 // a group's user ids
}

// folderTargets lists who can be graded in a folder: the category's groups
// for a group folder, the class list's students otherwise. names maps every
// class-list user id to a name.
func (a *api) folderTargets(ctx context.Context, org int64, f folder) ([]target, map[int64]string, error) {
	ms, err := a.classlist(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	names := map[int64]string{}
	var out []target
	for _, m := range ms {
		names[m.id()] = m.DisplayName
		if !isGroupFolder(f) && m.isStudent() {
			out = append(out, target{id: m.id(), name: m.DisplayName})
		}
	}
	if isGroupFolder(f) {
		if f.GroupTypeID == nil {
			return nil, nil, fail("UNSUPPORTED_FOLDER", "", "%q is a group assignment with no group category", f.Name)
		}
		gs, err := a.groups(ctx, org, *f.GroupTypeID)
		if err != nil {
			return nil, nil, err
		}
		for _, g := range gs {
			out = append(out, target{id: g.GroupID, name: g.Name, isGroup: true, members: g.Enrollments})
		}
	}
	return out, names, nil
}

// matchTarget finds who an argument names: an id, a group or user name
// fragment, or (in a group folder) a member's name, which picks their group.
func matchTarget(ts []target, names map[int64]string, arg string) ([]target, error) {
	arg = strings.TrimSpace(arg)
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, t := range ts {
			if t.id == id {
				return []target{t}, nil
			}
		}
		for _, t := range ts {
			for _, u := range t.members {
				if u == id {
					return []target{t}, nil
				}
			}
		}
		return nil, fail("NOT_FOUND", "list them with brightspace_submissions or brightspace_people.", "Nobody with id %d can be graded here", id)
	}
	low := strings.ToLower(arg)
	var exact, m []target
	for _, t := range ts {
		n := strings.ToLower(t.name)
		if n == low {
			exact = append(exact, t)
		}
		if strings.Contains(n, low) {
			m = append(m, t)
			continue
		}
		for _, u := range t.members {
			if strings.Contains(strings.ToLower(names[u]), low) {
				m = append(m, t)
				break
			}
		}
	}
	if len(exact) == 1 {
		return exact, nil
	}
	if len(m) == 0 {
		return nil, fail("NOT_FOUND", "list them with brightspace_submissions or brightspace_people.", "Nobody here matches %q", arg)
	}
	return m, nil
}

func oneTarget(ts []target, names map[int64]string, arg string) (target, error) {
	m, err := matchTarget(ts, names, arg)
	if err != nil {
		return target{}, err
	}
	if len(m) == 1 {
		return m[0], nil
	}
	var ns []string
	for i, t := range m {
		if i == 6 {
			ns = append(ns, "…")
			break
		}
		ns = append(ns, fmt.Sprintf("%d %s", t.id, t.name))
	}
	return target{}, invalid("%q matches several (%s). Use the id", arg, strings.Join(ns, "; "))
}

func (a *api) toolSubmissions(ctx context.Context, args map[string]string) (string, error) {
	status := strings.ToLower(strings.TrimSpace(args["status"]))
	switch status {
	case "", "all", "ungraded", "missing", "late":
	default:
		return "", invalid("status is all, ungraded, missing or late")
	}
	c, base, err := a.staffFolder(ctx, args["course"], args["assignment"])
	if err != nil {
		return "", err
	}
	f, err := a.folder(ctx, c.ID, base.ID)
	if err != nil {
		return "", err
	}
	es, err := a.allEntries(ctx, c.ID, base.ID)
	if err != nil {
		return "", err
	}
	ts, names, err := a.folderTargets(ctx, c.ID, base)
	if err != nil {
		return "", err
	}
	var keep map[int64]bool
	if w := strings.TrimSpace(args["who"]); w != "" {
		m, err := matchTarget(ts, names, w)
		if err != nil {
			return "", err
		}
		keep = map[int64]bool{}
		for _, t := range m {
			keep[t.id] = true
		}
	}
	due := parseTime(base.DueDate)
	den := ""
	if f.Assessment != nil && f.Assessment.ScoreDenominator != nil {
		den = "/" + points(f.Assessment.ScoreDenominator)
	}
	byID := map[int64]*entry{}
	for i := range es {
		byID[es[i].Entity.EntityID] = &es[i]
	}

	var b strings.Builder
	submitted, graded, published, late, missing := 0, 0, 0, 0, 0
	var rows []string
	for _, t := range ts {
		e := byID[t.id]
		has := e != nil && len(e.Submissions) > 0
		var last time.Time
		if has {
			submitted++
			last = parseTime(e.Submissions[len(e.Submissions)-1].SubmissionDate)
		} else {
			missing++
		}
		isLate := has && !due.IsZero() && last.After(due)
		if isLate {
			late++
		}
		fb := (*feedback)(nil)
		if e != nil {
			fb = e.Feedback
		}
		hasFb := fb != nil && (fb.Score != nil || richText(fb.Feedback) != "" || len(fb.RubricAssessments) > 0)
		if hasFb {
			graded++
			if fb.IsGraded {
				published++
			}
		}
		switch status {
		case "ungraded":
			if !has || hasFb {
				continue
			}
		case "missing":
			if has {
				continue
			}
		case "late":
			if !isLate {
				continue
			}
		}
		if keep != nil && !keep[t.id] {
			continue
		}
		var r strings.Builder
		fmt.Fprintf(&r, "%s (%d)", t.name, t.id)
		if !has {
			r.WriteString(" | not submitted")
		} else {
			fmt.Fprintf(&r, " | %s", when(last))
			if isLate {
				r.WriteString(" (late)")
			}
			if len(e.Submissions) > 1 {
				fmt.Fprintf(&r, ", %d submissions", len(e.Submissions))
			}
			s := e.Submissions[len(e.Submissions)-1]
			if t.isGroup && s.SubmittedBy.DisplayName != "" {
				r.WriteString(" by " + s.SubmittedBy.DisplayName)
			}
			var files []string
			for _, sub := range e.Submissions {
				for _, fl := range sub.Files {
					x := fmt.Sprintf("[%d] %s (%s", fl.FileID, fl.FileName, size(fl.Size))
					if fl.IsRead != nil && !*fl.IsRead {
						x += ", unread"
					}
					files = append(files, x+")")
				}
			}
			if len(files) > 0 {
				r.WriteString(" | " + strings.Join(files, ", "))
			}
			if txt := strings.TrimSpace(s.Comment.Text); txt != "" {
				r.WriteString(" | text: " + trunc(oneLine(txt), 150))
			}
		}
		if hasFb {
			state := "draft"
			if fb.IsGraded {
				state = "published"
			}
			fmt.Fprintf(&r, " | %s%s %s", points(fb.Score), den, state)
			for _, ra := range fb.RubricAssessments {
				if ra.OverallScore != nil {
					fmt.Fprintf(&r, ", rubric %s", points(ra.OverallScore))
				}
			}
			if txt := richText(fb.Feedback); txt != "" {
				r.WriteString(" | feedback: " + trunc(oneLine(txt), 160))
			}
		}
		rows = append(rows, r.String())
	}
	kind := "individual"
	if isGroupFolder(base) {
		kind = "group"
	}
	fmt.Fprintf(&b, "%s (%d) | %s | %s | due %s", base.Name, base.ID, c.Code, kind, when(due))
	if den != "" {
		fmt.Fprintf(&b, " | out of %s", strings.TrimPrefix(den, "/"))
	}
	fmt.Fprintf(&b, "\n%d submitted (%d late), %d missing, %d with feedback (%d published, %d draft)\n", submitted, late, missing, graded, published, graded-published)
	if f.Assessment != nil {
		for _, rb := range f.Assessment.Rubrics {
			fmt.Fprintf(&b, "Rubric %q (%d): see brightspace_assignment for its levels\n", rb.Name, rb.RubricID)
		}
	}
	for i, r := range rows {
		if i == 120 {
			fmt.Fprintf(&b, "… %d more (filter with status or who)\n", len(rows)-120)
			break
		}
		b.WriteString(r + "\n")
	}
	if len(rows) == 0 {
		b.WriteString("Nobody matches the filter.\n")
	}
	return strings.TrimSpace(b.String()), nil
}

// gradeItem is a gradebook column.
type gradeItem struct {
	ID             int64 `json:"Id"`
	Name           string
	MaxPoints      *float64
	Weight         *float64
	GradeType      string
	IsHidden       bool
	AssociatedTool *struct {
		ToolID     int64 `json:"ToolId"`
		ToolItemID int64 `json:"ToolItemId"`
	}
}

func (a *api) gradeItems(ctx context.Context, org int64) ([]gradeItem, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/grades/", org))
	if err != nil {
		return nil, err
	}
	var gs []gradeItem
	if err := a.getJSON(ctx, path, &gs); err != nil {
		return nil, err
	}
	return gs, nil
}

func findGradeItem(gs []gradeItem, arg string) (gradeItem, error) {
	arg = strings.TrimSpace(arg)
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, g := range gs {
			if g.ID == id {
				return g, nil
			}
		}
		return gradeItem{}, fail("NOT_FOUND", "list items with brightspace_gradebook.", "No grade item has id %d", id)
	}
	low := strings.ToLower(arg)
	var m []gradeItem
	for _, g := range gs {
		if strings.ToLower(g.Name) == low {
			return g, nil
		}
		if strings.Contains(strings.ToLower(g.Name), low) {
			m = append(m, g)
		}
	}
	switch len(m) {
	case 0:
		return gradeItem{}, fail("NOT_FOUND", "list items with brightspace_gradebook.", "No grade item matches %q", arg)
	case 1:
		return m[0], nil
	}
	var ns []string
	for _, g := range m {
		ns = append(ns, fmt.Sprintf("%d %s", g.ID, g.Name))
	}
	return gradeItem{}, invalid("%q matches several grade items (%s). Use the id", arg, strings.Join(ns, "; "))
}

// staffGrade is a grade value as staff read it.
type staffGrade struct {
	GradeObjectIdentifier string
	GradeObjectName       string
	PointsNumerator       *float64
	PointsDenominator     *float64
	DisplayedGrade        string
	Comments              struct{ Text, Html string }
	PrivateComments       struct{ Text, Html string }
	LastModified          string
	LastModifiedBy        *int64
}

func (g staffGrade) text() string {
	s := g.DisplayedGrade
	if g.PointsNumerator != nil {
		s = fmt.Sprintf("%s/%s", num(g.PointsNumerator), num(g.PointsDenominator))
		if g.DisplayedGrade != "" {
			s += " (" + g.DisplayedGrade + ")"
		}
	}
	if s == "" {
		s = "no grade"
	}
	if c := richText(&g.Comments); c != "" {
		s += " | comment: " + trunc(oneLine(c), 150)
	}
	if c := richText(&g.PrivateComments); c != "" {
		s += " | private: " + trunc(oneLine(c), 150)
	}
	return s
}

// paged follows a {Objects, Next} listing.
func paged[T any](ctx context.Context, a *api, path string, limit int) ([]T, error) {
	var out []T
	for page := 0; page < limit && path != ""; page++ {
		var r struct {
			Objects []T
			Next    *string
		}
		if err := a.getJSON(ctx, path, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Objects...)
		path = ""
		if r.Next != nil && *r.Next != "" {
			path = strings.TrimPrefix(*r.Next, a.site)
		}
	}
	return out, nil
}

type userGrade struct {
	User struct {
		Identifier  string
		DisplayName string
	}
	GradeValue *staffGrade
}

func (a *api) itemValues(ctx context.Context, org, item int64) ([]userGrade, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/grades/%d/values/", org, item))
	if err != nil {
		return nil, err
	}
	return paged[userGrade](ctx, a, path, 50)
}

// student finds one class-list member by id, code or name fragment.
func (a *api) student(ctx context.Context, org int64, arg string) (member, error) {
	ms, err := a.classlist(ctx, org)
	if err != nil {
		return member{}, err
	}
	return pickMember(ms, arg)
}

func toolName(t int64) string {
	switch t {
	case 2000:
		return "assignment"
	case 51000:
		return "quiz"
	}
	return ""
}

func (a *api) toolGradebook(ctx context.Context, args map[string]string) (string, error) {
	c, err := a.oneStaffCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	items, err := a.gradeItems(ctx, c.ID)
	if err != nil {
		return "", err
	}
	itemArg, whoArg := strings.TrimSpace(args["item"]), strings.TrimSpace(args["who"])
	var b strings.Builder
	switch {
	case itemArg == "" && whoArg == "":
		type stats struct {
			Average, Median, Minimum, Maximum *float64
		}
		st := parallel(items, func(g gradeItem) *stats {
			p, err := a.lePath(ctx, fmt.Sprintf("/%d/grades/%d/statistics", c.ID, g.ID))
			if err != nil {
				return nil
			}
			var s stats
			if a.getJSON(ctx, p, &s) != nil {
				return nil
			}
			return &s
		})
		fmt.Fprintf(&b, "%s: %d grade items\n", c, len(items))
		for i, g := range items {
			fmt.Fprintf(&b, "%s (%d) | max %s", g.Name, g.ID, points(g.MaxPoints))
			if g.Weight != nil && *g.Weight > 0 {
				fmt.Fprintf(&b, " | weight %s", points(g.Weight))
			}
			if g.AssociatedTool != nil {
				if n := toolName(g.AssociatedTool.ToolID); n != "" {
					fmt.Fprintf(&b, " | %s %d", n, g.AssociatedTool.ToolItemID)
				}
			}
			if s := st[i]; s != nil && s.Average != nil {
				fmt.Fprintf(&b, " | avg %.1f%%", *s.Average)
				if s.Median != nil {
					fmt.Fprintf(&b, ", median %.1f%%", *s.Median)
				}
			}
			if g.IsHidden {
				b.WriteString(" | hidden")
			}
			b.WriteByte('\n')
		}
	case itemArg != "":
		g, err := findGradeItem(items, itemArg)
		if err != nil {
			return "", err
		}
		vs, err := a.itemValues(ctx, c.ID, g.ID)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s (%d) | %s | max %s\n", g.Name, g.ID, c.Code, points(g.MaxPoints))
		low := strings.ToLower(whoArg)
		n := 0
		for _, v := range vs {
			if low != "" && v.User.Identifier != whoArg && !strings.Contains(strings.ToLower(v.User.DisplayName), low) {
				continue
			}
			n++
			fmt.Fprintf(&b, "%s (%s) | ", v.User.DisplayName, v.User.Identifier)
			if v.GradeValue == nil {
				b.WriteString("no grade\n")
				continue
			}
			b.WriteString(v.GradeValue.text())
			if t := parseTime(v.GradeValue.LastModified); !t.IsZero() {
				b.WriteString(" | " + when(t))
			}
			b.WriteByte('\n')
		}
		if n == 0 {
			b.WriteString("Nobody matches.\n")
		}
	default:
		m, err := a.student(ctx, c.ID, whoArg)
		if err != nil {
			return "", err
		}
		p, err := a.lePath(ctx, fmt.Sprintf("/%d/grades/values/%s/", c.ID, m.Identifier))
		if err != nil {
			return "", err
		}
		var gs []staffGrade
		if err := a.getJSON(ctx, p, &gs); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s (%s) | %s | %s\n", m.DisplayName, m.Identifier, m.ClasslistRoleDisplayName, c.Code)
		for _, g := range gs {
			fmt.Fprintf(&b, "%s: %s\n", g.GradeObjectName, g.text())
		}
		if p, err := a.lePath(ctx, fmt.Sprintf("/%d/grades/final/values/%s", c.ID, m.Identifier)); err == nil {
			var fin staffGrade
			if a.getJSON(ctx, p, &fin) == nil && fin.DisplayedGrade != "" {
				fmt.Fprintf(&b, "Final (%s): %s\n", fin.GradeObjectName, fin.text())
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}

type quiz struct {
	QuizID      int64 `json:"QuizId"`
	Name        string
	GradeItemID *int64 `json:"GradeItemId"`
	DueDate     string
}

type attempt struct {
	AttemptID     int64 `json:"AttemptId"`
	UserID        int64 `json:"UserId"`
	AttemptNumber int
	Score         *float64
	Started       string
	Completed     string
	IsPublished   bool
}

func (a *api) toolQuizResults(ctx context.Context, args map[string]string) (string, error) {
	cs, err := a.staffCourses(ctx, args["course"])
	if err != nil {
		return "", err
	}
	type hit struct {
		c   course
		q   quiz
		err error
	}
	arg := strings.TrimSpace(args["quiz"])
	hits := parallel(cs, func(c course) hit {
		p, err := a.lePath(ctx, fmt.Sprintf("/%d/quizzes/", c.ID))
		if err != nil {
			return hit{err: err}
		}
		qs, err := paged[quiz](ctx, a, p, 10)
		if err != nil {
			return hit{err: err}
		}
		var m []quiz
		low := strings.ToLower(arg)
		for _, q := range qs {
			if strconv.FormatInt(q.QuizID, 10) == arg || strings.ToLower(q.Name) == low {
				m = []quiz{q}
				break
			}
			if strings.Contains(strings.ToLower(q.Name), low) {
				m = append(m, q)
			}
		}
		switch len(m) {
		case 0:
			return hit{err: fail("NOT_FOUND", "", "No quiz matches %q in %s", arg, c.Code)}
		case 1:
			return hit{c: c, q: m[0]}
		}
		var ns []string
		for _, q := range m {
			ns = append(ns, fmt.Sprintf("%d %s", q.QuizID, q.Name))
		}
		return hit{err: invalid("%q matches several quizzes (%s). Use the id", arg, strings.Join(ns, "; "))}
	})
	var found []hit
	var firstErr error
	for _, h := range hits {
		if h.err == nil {
			found = append(found, h)
		} else if firstErr == nil || strings.HasPrefix(h.err.Error(), "INVALID_ARG") {
			firstErr = h.err
		}
	}
	if len(found) == 0 {
		return "", firstErr
	}
	if len(found) > 1 {
		var ns []string
		for _, h := range found {
			ns = append(ns, fmt.Sprintf("%s: %d %s", h.c.Code, h.q.QuizID, h.q.Name))
		}
		return "", invalid("%q is in several courses (%s). Pass course", arg, strings.Join(ns, "; "))
	}
	c, q := found[0].c, found[0].q
	p, err := a.lePath(ctx, fmt.Sprintf("/%d/quizzes/%d/attempts/", c.ID, q.QuizID))
	if err != nil {
		return "", err
	}
	ats, err := paged[attempt](ctx, a, p, 50)
	if err != nil {
		return "", err
	}
	ms, _ := a.classlist(ctx, c.ID)
	names := map[int64]string{}
	var students []member
	for _, m := range ms {
		names[m.id()] = m.DisplayName
		if m.isStudent() {
			students = append(students, m)
		}
	}
	took := func(at attempt) string {
		s, e := parseTime(at.Started), parseTime(at.Completed)
		if s.IsZero() || e.IsZero() {
			return "not finished"
		}
		return e.Sub(s).Round(time.Second).String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d) | %s | %d attempts | scores are quiz points\n", q.Name, q.QuizID, c.Code, len(ats))
	if w := strings.TrimSpace(args["who"]); w != "" {
		m, err := a.student(ctx, c.ID, w)
		if err != nil {
			return "", err
		}
		n := 0
		for _, at := range ats {
			if at.UserID != m.id() {
				continue
			}
			n++
			fmt.Fprintf(&b, "%s attempt %d: %s | %s | took %s", m.DisplayName, at.AttemptNumber, points(at.Score), when(parseTime(at.Started)), took(at))
			if !at.IsPublished {
				b.WriteString(" | not published")
			}
			b.WriteByte('\n')
		}
		if n == 0 {
			fmt.Fprintf(&b, "%s has no attempts.\n", m.DisplayName)
		}
		return strings.TrimSpace(b.String()), nil
	}
	best := map[int64]attempt{}
	count := map[int64]int{}
	for _, at := range ats {
		count[at.UserID]++
		cur, ok := best[at.UserID]
		if !ok || (at.Score != nil && (cur.Score == nil || *at.Score > *cur.Score)) {
			best[at.UserID] = at
		}
	}
	var uids []int64
	for u := range best {
		uids = append(uids, u)
	}
	sort.Slice(uids, func(i, j int) bool { return names[uids[i]] < names[uids[j]] })
	for _, u := range uids {
		at := best[u]
		n := names[u]
		if n == "" {
			n = "not enrolled now"
		}
		fmt.Fprintf(&b, "%s (%d) | best %s | %d attempt(s) | took %s\n", n, u, points(at.Score), count[u], took(at))
	}
	var none []string
	for _, m := range students {
		if _, ok := best[m.id()]; !ok {
			none = append(none, m.DisplayName)
		}
	}
	if len(none) > 0 {
		fmt.Fprintf(&b, "No attempt (%d): %s\n", len(none), trunc(strings.Join(none, ", "), 1500))
	}
	return strings.TrimSpace(b.String()), nil
}
