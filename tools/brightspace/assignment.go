package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type rubric struct {
	Name           string
	CriteriaGroups []struct {
		Name   string
		Levels []struct {
			ID   int64 `json:"Id"`
			Name string
		}
		Criteria []struct {
			ID    int64 `json:"Id"`
			Name  string
			Cells []struct {
				Description struct{ Text string }
				Points      *float64
				LevelID     int64 `json:"LevelId"`
			}
		}
	}
	OverallLevels []struct {
		ID   int64 `json:"Id"`
		Name string
	}
	RubricID int64 `json:"RubricId"`
}

// fullFolder is a folder with the details only the single-folder call gives.
type fullFolder struct {
	folder
	CustomInstructions struct{ Text string }
	Attachments        []fileRef
	Assessment         *struct {
		ScoreDenominator *float64
		Rubrics          []rubric
	}
}

func (a *api) folder(ctx context.Context, org, id int64) (*fullFolder, error) {
	path, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d", org, id))
	if err != nil {
		return nil, err
	}
	var f fullFolder
	if err := a.getJSON(ctx, path, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// assignFile is a file attached to an assignment, with how to download it.
type assignFile struct {
	fileRef
	kind string // instructor, submitted or feedback
	path string // API path (without version prefix handling: already complete)
}

func (a *api) assignFiles(ctx context.Context, org int64, f *fullFolder, e *entry) ([]assignFile, error) {
	var out []assignFile
	for _, at := range f.Attachments {
		p, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/attachments/%d", org, f.ID, at.FileID))
		if err != nil {
			return nil, err
		}
		out = append(out, assignFile{at, "instructor", p})
	}
	if e == nil {
		return out, nil
	}
	for _, s := range e.Submissions {
		for _, sf := range s.Files {
			p, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/submissions/%d/files/%d", org, f.ID, s.ID, sf.FileID))
			if err != nil {
				return nil, err
			}
			out = append(out, assignFile{sf, "submitted", p})
		}
	}
	if e.Feedback != nil {
		kind := "user"
		if strings.EqualFold(e.Entity.EntityType, "group") {
			kind = "group"
		}
		for _, ff := range e.Feedback.Files {
			p, err := a.lePath(ctx, fmt.Sprintf("/%d/dropbox/folders/%d/feedback/%s/%d/attachments/%d", org, f.ID, kind, e.Entity.EntityID, ff.FileID))
			if err != nil {
				return nil, err
			}
			out = append(out, assignFile{ff, "feedback", p})
		}
	}
	return out, nil
}

func findAssignFile(fs []assignFile, arg string) (assignFile, error) {
	arg = strings.TrimSpace(arg)
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, f := range fs {
			if f.FileID == id {
				return f, nil
			}
		}
		return assignFile{}, fail("NOT_FOUND", "list the files with brightspace_assignment.", "No file has id %d in this assignment", id)
	}
	low := strings.ToLower(arg)
	var m []assignFile
	for _, f := range fs {
		if strings.Contains(strings.ToLower(f.FileName), low) {
			m = append(m, f)
		}
	}
	switch len(m) {
	case 0:
		return assignFile{}, fail("NOT_FOUND", "list the files with brightspace_assignment.", "No file matches %q", arg)
	case 1:
		return m[0], nil
	}
	var names []string
	for _, f := range m {
		names = append(names, fmt.Sprintf("%d %s (%s)", f.FileID, f.FileName, f.kind))
	}
	return assignFile{}, invalid("%q matches several files (%s). Use the id", arg, strings.Join(names, "; "))
}

func points(p *float64) string {
	if p == nil {
		return "?"
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

func fileList(label string, fs []assignFile, kind string) string {
	var parts []string
	for _, f := range fs {
		if f.kind == kind {
			parts = append(parts, fmt.Sprintf("[%d] %s (%s)", f.FileID, f.FileName, size(f.Size)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return label + ": " + strings.Join(parts, ", ") + "\n"
}

// rubricText prints a rubric, with the user's assessment of it when there is one.
func rubricText(r rubric, as *rubricAssessment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rubric %q:\n", r.Name)
	for _, g := range r.CriteriaGroups {
		level := map[int64]string{}
		for _, l := range g.Levels {
			level[l.ID] = l.Name
		}
		for _, c := range g.Criteria {
			maxPts := 0.0
			for _, cell := range c.Cells {
				if cell.Points != nil && *cell.Points > maxPts {
					maxPts = *cell.Points
				}
			}
			var got *struct {
				level int64
				score *float64
				fb    string
			}
			if as != nil {
				for _, o := range as.CriteriaOutcome {
					if o.CriterionID == c.ID {
						got = &struct {
							level int64
							score *float64
							fb    string
						}{o.LevelID, o.Score, strings.TrimSpace(o.Feedback.Text)}
					}
				}
			}
			if got != nil {
				fmt.Fprintf(&b, "• %s: %s %s", trunc(c.Name, 90), level[got.level], points(got.score))
				if maxPts > 0 {
					fmt.Fprintf(&b, "/%s", strconv.FormatFloat(maxPts, 'f', -1, 64))
				}
				for _, cell := range c.Cells {
					if cell.LevelID == got.level && strings.TrimSpace(cell.Description.Text) != "" {
						b.WriteString(" (" + trunc(plain(cell.Description.Text), 90) + ")")
					}
				}
				if got.fb != "" {
					b.WriteString(" — " + trunc(got.fb, 300))
				}
				b.WriteByte('\n')
				continue
			}
			fmt.Fprintf(&b, "• %s:", trunc(c.Name, 90))
			for i, cell := range c.Cells {
				sep := " |"
				if i == 0 {
					sep = ""
				}
				fmt.Fprintf(&b, "%s %s %s", sep, level[cell.LevelID], points(cell.Points))
				if d := strings.TrimSpace(plain(cell.Description.Text)); d != "" {
					b.WriteString(" " + trunc(d, 70))
				}
			}
			b.WriteByte('\n')
		}
	}
	if as != nil && as.OverallScore != nil {
		name := ""
		for _, l := range r.OverallLevels {
			if l.ID == as.OverallLevel.LevelID {
				name = l.Name + " "
			}
		}
		fmt.Fprintf(&b, "Overall: %s%s", name, points(as.OverallScore))
		if t := strings.TrimSpace(as.OverallFeedback.Text); t != "" {
			b.WriteString(" — " + trunc(t, 300))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (a *api) toolAssignment(ctx context.Context, args map[string]string) (string, error) {
	c, err := a.resolveCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	fs, err := a.folders(ctx, c.ID)
	if err != nil {
		return "", err
	}
	base, err := findFolder(fs, args["assignment"])
	if err != nil {
		return "", err
	}
	f, err := a.folder(ctx, c.ID, base.ID)
	if err != nil {
		return "", err
	}
	f.GroupTypeID = base.GroupTypeID
	e, known, err := a.myEntry(ctx, c.ID, base)
	if err != nil {
		return "", err
	}
	files, err := a.assignFiles(ctx, c.ID, f, e)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	kinds := map[int]string{0: "files", 1: "online text", 2: "files or text", 3: "in person"}
	fmt.Fprintf(&b, "%s (id %d) | %s | due %s | takes %s\n", f.Name, f.ID, c.Code, when(parseTime(f.DueDate)), kinds[f.SubmissionType])
	if t := strings.TrimSpace(f.CustomInstructions.Text); t != "" {
		b.WriteString("Instructions: " + trunc(t, 1200) + "\n")
	}
	b.WriteString(fileList("Instructor files", files, "instructor"))

	var assessment *rubricAssessment
	if e != nil && e.Feedback != nil && len(e.Feedback.RubricAssessments) > 0 {
		assessment = &e.Feedback.RubricAssessments[0]
	}
	if f.Assessment != nil {
		for _, r := range f.Assessment.Rubrics {
			var as *rubricAssessment
			if assessment != nil && assessment.RubricID == r.RubricID {
				as = assessment
			}
			b.WriteString(trunc(rubricText(r, as), 3500) + "\n")
		}
	}

	switch {
	case !known:
		b.WriteString("Submissions: this is a group assignment and your group couldn't be identified, so nothing is shown.\n")
	case e != nil && e.NoGroup:
		b.WriteString("Submissions: this is a group assignment and you aren't in any of its groups\n")
	case e == nil || len(e.Submissions) == 0:
		b.WriteString("Submissions: none yet\n")
	default:
		if strings.EqualFold(e.Entity.EntityType, "group") {
			b.WriteString("(group assignment: your group's submissions)\n")
		}
		fmt.Fprintf(&b, "Submissions: %d\n", len(e.Submissions))
		for i, s := range e.Submissions {
			fmt.Fprintf(&b, "#%d %s", i+1, when(parseTime(s.SubmissionDate)))
			if t := strings.TrimSpace(s.Comment.Text); t != "" {
				b.WriteString(" | text: " + trunc(t, 600))
			}
			var names []string
			for _, sf := range s.Files {
				names = append(names, fmt.Sprintf("[%d] %s (%s)", sf.FileID, sf.FileName, size(sf.Size)))
			}
			if len(names) > 0 {
				b.WriteString(" | files: " + strings.Join(names, ", "))
			}
			b.WriteByte('\n')
		}
	}

	if e != nil && e.Feedback != nil {
		fb := e.Feedback
		if fb.Score != nil || fb.IsGraded {
			fmt.Fprintf(&b, "Grade: %s", points(fb.Score))
			if f.Assessment != nil && f.Assessment.ScoreDenominator != nil {
				fmt.Fprintf(&b, "/%s", points(f.Assessment.ScoreDenominator))
			}
			b.WriteByte('\n')
		}
		if fb.Feedback != nil && strings.TrimSpace(fb.Feedback.Text) != "" {
			b.WriteString("Feedback: " + trunc(strings.TrimSpace(fb.Feedback.Text), 1500) + "\n")
		}
		b.WriteString(fileList("Feedback files", files, "feedback"))
	}
	return strings.TrimSpace(b.String()), nil
}

// downloadAssignFile saves one of an assignment's files.
func (a *api) downloadAssignFile(ctx context.Context, args map[string]string, c course) (string, error) {
	fs, err := a.folders(ctx, c.ID)
	if err != nil {
		return "", err
	}
	base, err := findFolder(fs, args["assignment"])
	if err != nil {
		return "", err
	}
	f, err := a.folder(ctx, c.ID, base.ID)
	if err != nil {
		return "", err
	}
	e, _, err := a.myEntry(ctx, c.ID, base)
	if err != nil {
		return "", err
	}
	files, err := a.assignFiles(ctx, c.ID, f, e)
	if err != nil {
		return "", err
	}
	af, err := findAssignFile(files, args["file"])
	if err != nil {
		return "", err
	}
	resp, err := a.send(ctx, func(string) (*http.Request, error) { return http.NewRequest("GET", a.site+af.path, nil) })
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", statusErr(resp, "The file")
	}
	dest := args["dest"]
	if dest == "" {
		dest = defaultDest(c)
	}
	path, n, err := saveTo(dest, remoteName(resp, af.FileName), resp.Body)
	if err != nil {
		return "", fail("API_ERROR", "", "Couldn't save the file: %v", err)
	}
	return fmt.Sprintf("Saved %s (%s)", path, size(n)), nil
}
