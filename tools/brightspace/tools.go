package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// folder is an assignment folder (dropbox).
type folder struct {
	ID             int64 `json:"Id"`
	Name           string
	DueDate        string
	IsHidden       bool
	SubmissionType int // 0 file, 1 text, 2 file or text, 3 observed in person
	// AllowableFileType is 0 for any file; CustomAllowableFileTypes lists extensions.
	AllowableFileType        int
	CustomAllowableFileTypes string
	GroupTypeID              *int64 `json:"GroupTypeId"`
	Availability             *struct {
		StartDate string
		EndDate   string
	}
}

func (a *api) folders(ctx context.Context, org int64) ([]folder, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/", org))
	if err != nil {
		return nil, err
	}
	var fs []folder
	if err := a.getJSON(ctx, path, &fs); err != nil {
		return nil, err
	}
	return fs, nil
}

// fileRef is a file the API lists: an attachment, a submitted file or a feedback file.
type fileRef struct {
	FileID   int64 `json:"FileId"`
	FileName string
	Size     int64
}

// submission is one of the user's own submissions to a folder.
type submission struct {
	ID             int64 `json:"Id"`
	SubmissionDate string
	Comment        struct{ Text string }
	Files          []fileRef
}

type rubricAssessment struct {
	RubricID        int64 `json:"RubricId"`
	OverallScore    *float64
	OverallFeedback struct{ Text string }
	OverallLevel    struct {
		LevelID int64 `json:"LevelId"`
	}
	CriteriaOutcome []struct {
		CriterionID int64 `json:"CriterionId"`
		LevelID     int64 `json:"LevelId"`
		Score       *float64
		Feedback    struct{ Text string }
	}
}

type feedback struct {
	Score             *float64
	Feedback          *struct{ Text, Html string }
	Files             []fileRef
	IsGraded          bool
	RubricAssessments []rubricAssessment
}

// entry is the status of one user or group in a folder.
type entry struct {
	Entity struct {
		EntityID   int64 `json:"EntityId"`
		EntityType string
	}
	Status      int
	Feedback    *feedback
	Submissions []submission
	NoGroup     bool `json:"-"` // a group folder in which the user has no group
}

// userID is the signed-in user's id.
func (a *api) userID(ctx context.Context) (int64, error) {
	a.mu.Lock()
	id := a.uid
	a.mu.Unlock()
	if id != 0 {
		return id, nil
	}
	path, err := a.lpPath(ctx, "/users/whoami")
	if err != nil {
		return 0, err
	}
	var w struct{ Identifier string }
	if err := a.getJSON(ctx, path, &w); err != nil {
		return 0, err
	}
	id, err = strconv.ParseInt(w.Identifier, 10, 64)
	if err != nil {
		return 0, fail("API_ERROR", "", "The site gave no user id")
	}
	a.mu.Lock()
	a.uid = id
	a.mu.Unlock()
	return id, nil
}

// myGroups are the ids of the user's groups in a group category.
func (a *api) myGroups(ctx context.Context, org, category int64) (map[int64]bool, error) {
	uid, err := a.userID(ctx)
	if err != nil {
		return nil, err
	}
	path, err := a.lpPath(ctx, fmt.Sprintf("/%d/groupcategories/%d/groups/", org, category))
	if err != nil {
		return nil, err
	}
	var gs []struct {
		GroupID     int64 `json:"GroupId"`
		Enrollments []int64
	}
	if err := a.getJSON(ctx, path, &gs); err != nil {
		return nil, err
	}
	mine := map[int64]bool{}
	for _, g := range gs {
		for _, u := range g.Enrollments {
			if u == uid {
				mine[g.GroupID] = true
			}
		}
	}
	return mine, nil
}

// myEntry returns the user's own entry in a folder. For a group folder the
// site lists every group, so only the user's group is kept. known=false means
// the user's group couldn't be found, and nothing is returned.
func (a *api) myEntry(ctx context.Context, org int64, f folder) (e *entry, known bool, err error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/submissions/mysubmissions/", org, f.ID))
	if err != nil {
		return nil, false, err
	}
	var all []entry
	if err := a.getJSON(ctx, path, &all); err != nil {
		return nil, false, err
	}
	uid, err := a.userID(ctx)
	if err != nil {
		return nil, false, err
	}
	var groups map[int64]bool
	for i := range all {
		en := &all[i]
		switch strings.ToLower(en.Entity.EntityType) {
		case "user":
			if en.Entity.EntityID == uid {
				return en, true, nil
			}
		case "group":
			if groups == nil {
				if f.GroupTypeID == nil {
					return nil, false, nil
				}
				if groups, err = a.myGroups(ctx, org, *f.GroupTypeID); err != nil {
					return nil, false, nil
				}
			}
			if groups[en.Entity.EntityID] {
				return en, true, nil
			}
		}
	}
	// No entry at all (nothing submitted yet) is a known, empty state.
	if len(all) == 0 {
		return &entry{}, true, nil
	}
	if groups != nil || f.GroupTypeID == nil {
		return &entry{NoGroup: groups != nil}, groups != nil, nil
	}
	return nil, false, nil
}

// mySubmissions lists the user's own submissions to a folder, oldest first.
func (a *api) mySubmissions(ctx context.Context, org int64, f folder) ([]submission, error) {
	e, _, err := a.myEntry(ctx, org, f)
	if err != nil || e == nil {
		return nil, err
	}
	return e.Submissions, nil
}

// eachCourse runs fn for every course, a few at a time, in order.
func eachCourse[T any](cs []course, fn func(course) T) []T {
	out := make([]T, len(cs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = fn(c)
		}()
	}
	wg.Wait()
	return out
}

func (a *api) toolCourses(ctx context.Context, args map[string]string) (string, error) {
	cs, err := a.allCourses(ctx)
	if err != nil {
		return "", err
	}
	all := args["all"] == "true"
	var b strings.Builder
	n := 0
	for _, c := range cs {
		if !all && !c.Current {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d | %s | %s", c.ID, c.Code, c.Name)
		if !c.Current {
			b.WriteString(" (not current)")
		}
		b.WriteByte('\n')
	}
	if n == 0 {
		return "No current courses. Use all=true for past ones.", nil
	}
	return strings.TrimSpace(b.String()), nil
}

type dueItem struct {
	at     time.Time
	course string
	name   string
	kind   string
	note   string
}

func (a *api) toolDue(ctx context.Context, args map[string]string) (string, error) {
	days := 14
	if v := args["days"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			return "", invalid("days must be a number from 1 to 365")
		}
		days = n
	}
	cs, err := a.courseOrCurrent(ctx, args["course"])
	if err != nil {
		return "", err
	}
	now := a.now()
	until := now.AddDate(0, 0, days)
	type result struct {
		items []dueItem
		err   error
	}
	results := eachCourse(cs, func(c course) result {
		var r result
		fs, err := a.folders(ctx, c.ID)
		if err != nil {
			r.err = err
			return r
		}
		for _, f := range fs {
			due := parseTime(f.DueDate)
			if f.IsHidden || due.IsZero() || due.Before(now) || due.After(until) {
				continue
			}
			it := dueItem{at: due, course: c.Code, name: f.Name, kind: "assignment"}
			if ss, err := a.mySubmissions(ctx, c.ID, f); err == nil {
				if len(ss) > 0 {
					it.note = fmt.Sprintf("submitted %d×", len(ss))
				} else {
					it.note = "not submitted"
				}
			}
			r.items = append(r.items, it)
		}
		if path, err := a.lePath(ctx, fmt.Sprintf("/%d/quizzes/", c.ID)); err == nil {
			var qs struct {
				Objects []struct {
					Name    string
					DueDate string
				}
			}
			if a.getJSON(ctx, path, &qs) == nil {
				for _, q := range qs.Objects {
					due := parseTime(q.DueDate)
					if due.IsZero() || due.Before(now) || due.After(until) {
						continue
					}
					r.items = append(r.items, dueItem{at: due, course: c.Code, name: q.Name, kind: "quiz"})
				}
			}
		}
		return r
	})
	var items []dueItem
	var notes []string
	for i, r := range results {
		items = append(items, r.items...)
		if r.err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", cs[i].Code, r.err))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s | %s | %s (%s", when(it.at), it.course, it.name, it.kind)
		if it.note != "" {
			b.WriteString(", " + it.note)
		}
		b.WriteString(")\n")
	}
	if len(items) == 0 {
		fmt.Fprintf(&b, "Nothing due in the next %d days.\n", days)
	}
	for _, n := range notes {
		b.WriteString("skipped " + n + "\n")
	}
	return strings.TrimSpace(b.String()), nil
}

type gradeValue struct {
	GradeObjectName     string
	DisplayedGrade      string
	PointsNumerator     *float64
	PointsDenominator   *float64
	WeightedNumerator   *float64
	WeightedDenominator *float64
	GradeObjectTypeName string
	Comments            struct{ Text string }
}

func (a *api) grades(ctx context.Context, org int64) ([]gradeValue, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/grades/values/myGradeValues/", org))
	if err != nil {
		return nil, err
	}
	var gs []gradeValue
	if err := a.getJSON(ctx, path, &gs); err != nil {
		return nil, err
	}
	return gs, nil
}

func num(f *float64) string {
	if f == nil {
		return "?"
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

func (g gradeValue) line() string {
	s := g.GradeObjectName + ": "
	if g.DisplayedGrade != "" {
		s += g.DisplayedGrade
	} else {
		s += "not graded"
	}
	if g.PointsNumerator != nil && g.PointsDenominator != nil {
		s += fmt.Sprintf(" (%s/%s)", num(g.PointsNumerator), num(g.PointsDenominator))
	}
	return s
}

func (a *api) toolGrades(ctx context.Context, args map[string]string) (string, error) {
	cs, err := a.courseOrCurrent(ctx, args["course"])
	if err != nil {
		return "", err
	}
	single := strings.TrimSpace(args["course"]) != ""
	type result struct {
		gs  []gradeValue
		err error
	}
	results := eachCourse(cs, func(c course) result {
		gs, err := a.grades(ctx, c.ID)
		return result{gs, err}
	})
	var b strings.Builder
	for i, r := range results {
		c := cs[i]
		if r.err != nil {
			fmt.Fprintf(&b, "%s: %v\n", c.Code, r.err)
			continue
		}
		if single {
			fmt.Fprintf(&b, "%s\n", c)
			shown := 0
			for _, g := range r.gs {
				if g.DisplayedGrade == "" {
					continue
				}
				if shown == 60 {
					b.WriteString("…\n")
					break
				}
				b.WriteString("  " + g.line())
				if c := strings.TrimSpace(g.Comments.Text); c != "" {
					b.WriteString(" | comment: " + trunc(c, 300))
				}
				b.WriteString("\n")
				shown++
			}
			if shown == 0 {
				b.WriteString("  no grades yet\n")
			}
			continue
		}
		graded := 0
		final := ""
		for _, g := range r.gs {
			if g.DisplayedGrade != "" {
				graded++
			}
			n := strings.ToLower(g.GradeObjectName)
			if strings.Contains(n, "final") || strings.Contains(n, "total") || strings.Contains(n, "definitiva") {
				final = g.line()
			}
		}
		if final != "" {
			fmt.Fprintf(&b, "%s | %s | %d graded\n", c.Code, final, graded)
		} else {
			fmt.Fprintf(&b, "%s | %d graded items\n", c.Code, graded)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func (a *api) toolAnnouncements(ctx context.Context, args map[string]string) (string, error) {
	days := 14
	if v := args["days"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			return "", invalid("days must be a number from 1 to 365")
		}
		days = n
	}
	cs, err := a.courseOrCurrent(ctx, args["course"])
	if err != nil {
		return "", err
	}
	since := a.now().AddDate(0, 0, -days)
	type news struct {
		Title       string
		Body        struct{ Html, Text string }
		StartDate   string
		IsPublished bool
	}
	type result struct {
		items []news
		err   error
	}
	results := eachCourse(cs, func(c course) result {
		path, err := a.lePath(ctx, fmt.Sprintf("/%d/news/", c.ID))
		if err != nil {
			return result{err: err}
		}
		var ns []news
		err = a.getJSON(ctx, path, &ns)
		return result{ns, err}
	})
	var b strings.Builder
	for i, r := range results {
		c := cs[i]
		if r.err != nil {
			fmt.Fprintf(&b, "%s: %v\n", c.Code, r.err)
			continue
		}
		ns := r.items
		sort.Slice(ns, func(i, j int) bool { return parseTime(ns[i].StartDate).After(parseTime(ns[j].StartDate)) })
		shown := 0
		for _, n := range ns {
			t := parseTime(n.StartDate)
			if t.Before(since) || shown == 10 {
				continue
			}
			body := n.Body.Text
			if body == "" {
				body = plain(n.Body.Html)
			}
			fmt.Fprintf(&b, "%s | %s | %s\n%s\n\n", when(t), c.Code, n.Title, trunc(strings.TrimSpace(body), 500))
			shown++
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return fmt.Sprintf("No announcements in the last %d days.", days), nil
	}
	return out, nil
}
