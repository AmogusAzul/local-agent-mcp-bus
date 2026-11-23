# Brightspace: full announcements and courses you teach

Status: decided 2026-11-22, from Al día testers' feedback.

## Problem

1. `brightspace_announcements` hides syllabi and other long or old
   announcements. It looks back 14 days, keeps 10 per course and cuts each
   body at 500 characters, and there is no way to see more.
2. A student who is also a monitor (TA) in some sections asks "what's due next
   week" and gets errors for those sections. Student-only endpoints fail for
   courses where the user has an Instructor role (`course.Staff`).

## Decisions

### Announcements

1. **Time pages.** `brightspace_announcements` takes `days` (page length,
   default 14, 1–365) and a new `offset` (days back where the page ends,
   default 0). The page covers `[now-offset-days, now-offset)`. Example: the
   first two weeks are `offset=0`, the two weeks before are `offset=14`.
   The first page (`offset=0`) has no upper bound, as before, so news
   scheduled for later stays visible to staff.
2. **Optional limit.** A new `limit` arg (per course). Without it, the limit is
   10 per course, the same as today. The model can pass a large value, up to
   200.
3. **Say what was left out.** When announcements were cut by the limit, or
   older ones exist, the output ends with a line that tells the model how to
   ask for more, e.g. `More: offset=14` or `12 more in this window: limit=22`.
   Built as one line: `More: 3 more in this window: limit=13; older ones:
   offset=14` (either part alone when only one applies; ` days=N` is added
   when the page length isn't the default). `limit` is the largest per-course
   count in the window.
4. **Ids in the list.** Each item shows the announcement id. A body longer
   than 500 characters ends with `… (N more characters: brightspace_announcement
   course=<id> id=<id>)`.
5. **New read tool `brightspace_announcement`** (`course`, `id`, both
   required). It returns the title, date and full body as plain text, with
   attachment names and sizes if there are any. It is read-only, with no
   approval step.

Out of scope: syllabi that live in content topics or lesson descriptions
rather than in news. If one turns up, it gets its own spec.

### Courses you teach

6. Tools that read the student's own data (due dates, grades, submissions,
   feedback, "what changed") skip `Staff` courses **when no course is given**.
   The output ends with one line: `Skipped N courses where you are staff
   (monitor/instructor): CODE1, CODE2. Ask for one by name to include it.`
7. Naming the course still works as today; nothing is blocked.
8. Tools that make sense for staff too (announcements, content, people) keep
   every current course.
   As built: `brightspace_due`, `brightspace_grades` and `brightspace_updates`
   skip them. `brightspace_calendar` keeps them (its `myEvents` route works
   for staff, and classes and exams matter to them). Submissions and
   feedback are read through `brightspace_assignment`, which needs a course.
9. Each tool is checked by name to decide which list it belongs to, with a
   comment where the split happens. A test covers a mixed student/staff
   course list.

## Follow-up in Al día (not part of this change)

Bump the lamb dependency, classify `brightspace_announcement` as `read` in
`internal/aldia/tools.go` (its tests fail until then), then release.
