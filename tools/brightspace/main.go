// brightspace reads and submits work in a Brightspace (D2L) site. See spec/brightspace.md.
package main

import (
	"os"
	"time"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

// Set at build time with -ldflags -X (see build.sh).
var siteURL = "https://bloqueneon.uniandes.edu.co"

func newTools(a *api) []core.Tool {
	ts := []core.Tool{
		{
			Name:        "brightspace_courses",
			Description: "List the user's Brightspace courses (id | code | name). Current ones unless all=true.",
			Args:        []core.Arg{{Name: "all", Description: `"true" to include past courses`}},
			Execute:     a.toolCourses,
		},
		{
			Name:        "brightspace_due",
			Description: "Upcoming assignments and quizzes, soonest first, with submission status.",
			Args: []core.Arg{
				{Name: "days", Description: "look ahead this many days (default 14)"},
				{Name: "course", Description: "course id or name fragment (default: all current)"},
			},
			Execute: a.toolDue,
		},
		{
			Name:        "brightspace_grades",
			Description: "Grades for one course, or a one-line summary per current course.",
			Args:        []core.Arg{{Name: "course", Description: "course id or name fragment"}},
			Execute:     a.toolGrades,
		},
		{
			Name:        "brightspace_announcements",
			Description: "Recent course announcements as plain text.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment (default: all current)"},
				{Name: "days", Description: "how far back (default 14)"},
			},
			Execute: a.toolAnnouncements,
		},
		{
			Name:        "brightspace_content",
			Description: "A course's module and topic tree, or one topic's text.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "topic", Description: "topic id or name fragment; omit for the tree"},
			},
			Execute: a.toolContent,
		},
		{
			Name:        "brightspace_assignment",
			Description: "One assignment: instructions, instructor files, rubric, what the user submitted, grade and feedback with the rubric result. Lists file ids for brightspace_download.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "assignment", Description: "assignment id or name fragment", Required: true},
			},
			Execute: a.toolAssignment,
		},
		{
			Name:        "brightspace_download",
			Description: "Save one file to a folder and return its path: a course file (topic) or an assignment's instructor, submitted or feedback file (assignment and file).",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "topic", Description: "course file topic id or name fragment"},
				{Name: "assignment", Description: "assignment id or name fragment"},
				{Name: "file", Description: "file id or name fragment from brightspace_assignment"},
				{Name: "dest", Description: "destination folder (default: Downloads/brightspace/<course>)"},
			},
			Execute: a.toolDownload,
		},
		{
			Name:        "brightspace_sync",
			Description: "Mirror a course's files into a folder as a background job; call again with job to follow it. Reruns skip unchanged files.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment (to start)"},
				{Name: "dest", Description: "destination folder (default: Downloads/brightspace/<course>)"},
				{Name: "module", Description: "only modules whose path contains this"},
				{Name: "types", Description: "extensions, comma-separated (e.g. pdf,pptx)"},
				{Name: "max_mb", Description: "skip files bigger than this"},
				{Name: "job", Description: "job id to follow"},
			},
			Execute: a.toolSync,
		},
		{
			Name: "brightspace_submit",
			Description: "Submit files and/or text to an assignment. Without confirm=true it only previews and sends nothing: " +
				"show the preview to the user and submit only after they agree. A submission can't be undone.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "assignment", Description: "assignment id or name fragment", Required: true},
				{Name: "files", Description: "full local paths, comma-separated"},
				{Name: "text", Description: "online text, or a comment when files are sent"},
				{Name: "confirm", Description: `"true" to actually submit`},
			},
			Execute: a.toolSubmit,
		},
		{
			Name:        "brightspace_calendar",
			Description: "Calendar events (classes, exams, deadlines) across current courses, soonest first.",
			Args: []core.Arg{
				{Name: "days", Description: "look ahead this many days (default 14)"},
				{Name: "course", Description: "course id or name fragment (default: all current)"},
			},
			Execute: a.toolCalendar,
		},
		{
			Name:        "brightspace_updates",
			Description: "What changed per course since the last call: announcements, grades, content, assignments, plus the site's unread counts.",
			Args:        []core.Arg{{Name: "course", Description: "course id or name fragment (default: all current)"}},
			Execute:     a.toolUpdates,
		},
		{
			Name:        "brightspace_people",
			Description: "A course's class list: name, id, role, groups. contact=true adds email and student code.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "group", Description: "only members of groups matching this"},
				{Name: "role", Description: "only this role (e.g. Estudiante)"},
				{Name: "contact", Description: `"true" for email and code`},
			},
			Execute: a.toolPeople,
		},
		{
			Name:        "brightspace_groups",
			Description: "A course's group categories and groups: sizes, self-enrollment window, which is yours; members with category.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "category", Description: "category id or name fragment, to list members"},
			},
			Execute: a.toolGroups,
		},
		{
			Name: "brightspace_api",
			Description: "Any Brightspace (Valence) API call, for what no other tool covers. GET returns trimmed JSON; " +
				"other methods only preview until confirm=true (show the preview to the user first). path=help lists routes.",
			Args: []core.Arg{
				{Name: "path", Description: "route like le/{courseId}/news/ (versions added), or help", Required: true},
				{Name: "method", Description: "GET (default), POST, PUT, DELETE, PATCH"},
				{Name: "query", Description: "a=1&b=2"},
				{Name: "body", Description: "JSON, or @path to a JSON file"},
				{Name: "max", Description: "reply size cap in chars (default 6000)"},
				{Name: "confirm", Description: `"true" to send a write`},
			},
			Execute: a.toolAPI,
		},
		{
			Name: "brightspace_web",
			Description: "A Brightspace page as text with its forms and links, for features without an API (attendance, email). " +
				"With form/fields it previews a form submit; confirm=true sends it as the user.",
			Args: []core.Arg{
				{Name: "url", Description: "page path like /d2l/home/123, or a full site URL", Required: true},
				{Name: "form", Description: "form number or name on that page, to submit it"},
				{Name: "fields", Description: "fields to set: a=1&b=2 or a JSON object"},
				{Name: "max", Description: "reply size cap in chars (default 6000)"},
				{Name: "confirm", Description: `"true" to send`},
			},
			Execute: a.toolWeb,
		},
	}
	if !a.staff {
		return ts
	}
	for i := range ts {
		if ts[i].Name == "brightspace_sync" {
			ts[i].Args = append(ts[i].Args, core.Arg{Name: "assignment", Description: "staff: mirror this assignment's submissions instead"})
		}
	}
	return append(ts, staffTools(a)...)
}

// staffTools are registered only with BRIGHTSPACE_STAFF=1. See spec/brightspace-ta.md.
func staffTools(a *api) []core.Tool {
	course := core.Arg{Name: "course", Description: "course id or name fragment (staff courses)"}
	return []core.Tool{
		{
			Name:        "brightspace_staff",
			Description: "Staff overview per taught course: assignments with submitted, graded and unread counts; ungraded quizzes.",
			Args:        []core.Arg{course},
			Execute:     a.toolStaff,
		},
		{
			Name:        "brightspace_submissions",
			Description: "Staff: every user/group in one assignment: submitted at (late marked), file ids, score, draft/published, feedback.",
			Args: []core.Arg{
				course,
				{Name: "assignment", Description: "assignment id or name fragment", Required: true},
				{Name: "status", Description: "all (default), ungraded, missing, late"},
				{Name: "who", Description: "id, group or name fragment"},
			},
			Execute: a.toolSubmissions,
		},
		{
			Name:        "brightspace_gradebook",
			Description: "Staff: grade items with stats; with item, everyone's value and comments; with who, one student's row.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment (staff courses)", Required: true},
				{Name: "item", Description: "grade item id or name fragment"},
				{Name: "who", Description: "student id or name fragment"},
			},
			Execute: a.toolGradebook,
		},
		{
			Name:        "brightspace_quiz_results",
			Description: "Staff: a quiz's attempts: best score, attempts and time per student, who didn't take it; or one student's attempts.",
			Args: []core.Arg{
				course,
				{Name: "quiz", Description: "quiz id or name fragment", Required: true},
				{Name: "who", Description: "student id or name fragment"},
			},
			Execute: a.toolQuizResults,
		},
		{
			Name: "brightspace_feedback",
			Description: "Staff: preview, then with confirm=true save score, feedback text and rubric levels for a user/group, or rows from a CSV/JSON file. " +
				"Saved as a draft (hidden) unless publish=true; publish_all=true publishes the assignment's drafts. Show the preview and wait for the user.",
			Args: []core.Arg{
				course,
				{Name: "assignment", Description: "assignment id or name fragment", Required: true},
				{Name: "who", Description: "user/group id, group name, or name fragment"},
				{Name: "score", Description: "number (default: rubric total, else kept)"},
				{Name: "text", Description: "feedback, light markdown (default: kept)"},
				{Name: "rubric", Description: "criterion=level pairs, comma-separated"},
				{Name: "from", Description: "CSV/JSON file with who,score,text,rubric[,publish] rows (max 200)"},
				{Name: "publish", Description: `"true" to release to students, "false" to make a draft`},
				{Name: "publish_all", Description: `"true" to publish every draft here`},
				{Name: "confirm", Description: `"true" to write`},
			},
			Execute: a.toolFeedback,
		},
		{
			Name: "brightspace_grade",
			Description: "Staff: preview, then with confirm=true set a numeric grade item's value and comments for a student, or rows from a CSV/JSON file. " +
				"For assignment grades use brightspace_feedback. Show the preview and wait for the user.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment (staff courses)"},
				{Name: "item", Description: "grade item id or name fragment", Required: true},
				{Name: "who", Description: "student id, code or name fragment"},
				{Name: "value", Description: "points (default: kept)"},
				{Name: "comment", Description: "comment students see (default: kept)"},
				{Name: "private", Description: "staff-only comment (default: kept)"},
				{Name: "from", Description: "CSV/JSON file with who,value,comment,private rows (max 200)"},
				{Name: "confirm", Description: `"true" to write`},
			},
			Execute: a.toolGrade,
		},
		{
			Name:        "brightspace_announce",
			Description: "Staff: preview, then with confirm=true post a course announcement (markdown body, optional later start, attachments). Show the preview and wait for the user.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment (staff courses)", Required: true},
				{Name: "title", Description: "title", Required: true},
				{Name: "body", Description: "text, light markdown", Required: true},
				{Name: "start", Description: "show from, e.g. 2026-11-20 08:00 (default now)"},
				{Name: "files", Description: "attachments: full local paths, comma-separated"},
				{Name: "confirm", Description: `"true" to post`},
			},
			Execute: a.toolAnnounce,
		},
		{
			Name:        "brightspace_extension",
			Description: "Staff: preview, then with confirm=true give a student special access (own due and/or close date) on an assignment.",
			Args: []core.Arg{
				course,
				{Name: "assignment", Description: "assignment id or name fragment", Required: true},
				{Name: "who", Description: "student id, code or name fragment", Required: true},
				{Name: "due", Description: "new due date, e.g. 2026-11-22 23:59"},
				{Name: "end", Description: "new close date"},
				{Name: "confirm", Description: `"true" to save`},
			},
			Execute: a.toolExtension,
		},
	}
}

func main() {
	s := newSession(siteURL)
	a := newAPI(s)
	a.jobWait = 30 * time.Second
	a.staff = os.Getenv("BRIGHTSPACE_STAFF") == "1"
	core.Serve("brightspace", "0.1.0", newTools(a)...)
}
