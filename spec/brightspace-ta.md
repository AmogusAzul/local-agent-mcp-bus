# `brightspace`: stage 2, TA (monitor) tools and the remaining student pendings

Extends `spec/brightspace.md`. Read that first: session, errors, formatting and the
confirm pattern carry over unchanged.

## Problem

The user is a student in most courses and a **Monitor** (TA) in a few
sections. The tool only does student things, so grading a
Taller, checking who submitted, or posting the office-hours announcement still
means clicking through Bloque Neón. Stage 1 also left pendings: group submissions
fail (the 2026-11-13 404), and groups, a change digest, the calendar and quizzes
were deferred.

## Findings

A GET-only sweep on 2026-11-17 (full table in the local
`references/brightspace-endpoints-2026-11-17.md`):

- **Role detection.** `myenrollments` carries `Access.ClasslistRoleName`
  (`Estudiante`, `Monitor`) and `Access.LISRoles` (`…/Instructor` for Monitor).
- **Staff reads work with the same bearer token**: every user's or group's
  submissions per folder (files, dates, score, feedback, rubric results, publish
  state), folder totals (submitted, with feedback, unread files), grade items and
  every student's value (with private comments), grade statistics, final calculated
  grade, all quiz attempts with scores, rubric assessments per user
  (`le/{O}/assessment?...`), group categories with members, special access lists,
  hidden news and content, the course file area, and `updates/myUpdates`
  (unread submissions, ungraded quizzes).
- **The roster with emails and student codes** (`le/{O}/classlist/`) answers to
  students too.
- **The Monitor passes the API's permission checks for writes.** Sending the same
  write from a student course and a Monitor course, aimed at ids that don't exist,
  gave student 403 and Monitor 404/409 for grade values, special access, rubric
  assessments, calendar events, folder edits and group enrollments. The user has
  already posted a news item. A feedback POST to an entity that
  isn't a student or group answered 200: the feedback route doesn't validate the
  entity, so tool code must.
- The Monitor reaches every staff page in the UI (evaluate, publish feedback, import
  grades, new activity, new quiz, news, groups, calendar, attendance in the menu).
- **No API** for attendance, email, or the question library. Discussions exist but
  no current course has a forum.
- **The 2026-11-13 submit 404 is a group folder.** The folder has `DropboxType`
  1 and a `GroupTypeId`; group folders take
  `POST .../submissions/group/{groupId}`, not `mysubmissions`.

## Decisions

- **Goal: anything the user can do on the site, the MCP can do.** Three layers, so
  full coverage doesn't cost a tool per feature:
  1. **Curated tools** (below) for the frequent flows, with compact output.
  2. **`brightspace_api`, a generic Valence call** for everything else: `method`,
     `path` (`le/{v}/…`, versions filled in), optional `body` or `@file`, `query`.
     GET runs directly and returns trimmed JSON (long fields cut, `max` chars).
     Anything else is preview + `confirm`, like every write. Valence has a few
     hundred routes; the tool description points to `brightspace_api` with
     `path=help` for a compact route catalog grouped by area (kept in the binary),
     so the model doesn't need the docs.
  3. **`brightspace_web`, the site's own pages and endpoints**, for features with
     no Valence route: attendance, email to the class, quiz question grading,
     discussion moderation, "mark as read", the evaluation-experience actions. It
     GETs a page as text (forms and their fields listed) and POSTs a form with the
     cookie and XSRF token, again preview + `confirm`. Each feature that gets real
     use graduates to a curated tool once its requests are recorded in a spec.
  Every layer acts as the user, with the user's permissions; the site decides what's
  allowed.
- **Same binary, staff tools opt-in.** The TA tools add about 3 KB to `tools/list`.
  Students don't need them, so they're registered only when the server starts with
  `BRIGHTSPACE_STAFF=1` (set in the Desktop config entry). Without it the server is
  stage 1 plus the student fixes below.
- **Staff tools check the role per course.** A staff tool in a course where the user
  isn't staff (no `Instructor` LIS role) answers `NOT_STAFF` before any other call.
  `course` matching for staff tools only considers staff courses, so a course code
  shared by several taught sections resolves to those sections, and an ambiguous match lists them.
- **Every write is preview + `confirm="true"`**, as `brightspace_submit`. The
  preview shows exactly what changes per student (old → new), and nothing is sent
  without confirm. Tool descriptions tell the model to show the preview and wait.
- **Grading is draft-first.** `brightspace_feedback` saves feedback unpublished
  (`IsGraded=false`) unless `publish="true"`. Students see nothing until the user (or
  a later `publish`) releases it. Publishing a whole folder is its own confirmed
  call.
- **Batch from a file.** Grading 10 groups one call at a time is slow and costly.
  `brightspace_feedback` and `brightspace_grade` take either one target or `from`, a
  local CSV/JSON file (`entity, score, feedback[, criterion=level…]`). The preview
  lists every row; the write runs sequentially, stops at the first error, and
  reports what was written. Capped at 200 rows per call, human-scale pacing.
- **Student data stays minimal.** Replies identify students by name and id. Email
  and student code appear only in `brightspace_people` and only with
  `contact="true"`. Private grade comments appear only in staff tools. This data
  goes to the MCP client's model, which the user accepts by enabling staff tools.
- **Bulk-download submissions** reuses the `brightspace_sync` job machinery, one
  folder per user/group (`T3-G 4/`), with the same manifest and resume. If a GET of a
  submission file turns out to mark it read, the reply says so; marking read is
  harmless.
- **Student fixes ship in this stage:**
  - Group folders submit through `submissions/group/{groupId}`, with the user's group
    found as `brightspace_assignment` already does. The preview names the group. If
    the group can't be determined, the preview refuses (`UNSUPPORTED_FOLDER`) instead
    of failing at confirm. Group folders read the group's submissions for the
    read-back.
  - Any remaining 404 at confirm maps to `UNSUPPORTED_FOLDER` with "submit it on the
    site" as the hint.
- **New error code** `NOT_STAFF`. `CONFLICT` when a write finds the value changed
  since the preview (someone else graded meanwhile): the preview carries the
  `LastModified` it saw.

## Interface

Student and shared (always on):

| Tool | Does | Args |
|---|---|---|
| `brightspace_calendar` | Calendar events across current courses (classes, exams, deadlines the professor added), soonest first. | `days` (default 14), `course` |
| `brightspace_updates` | What's new per course since the last call: announcements, grades, feedback, content, due items; plus `myUpdates` counts. Snapshot kept next to the cache. | `course` |
| `brightspace_people` | Course roster: name, role, group; `contact=true` adds email and code. `group` filters to a group. | `course` (req), `group`, `role`, `contact` |
| `brightspace_groups` | Group categories and groups, members, seats left, self-enrollment window; marks the user's group. | `course` (req), `category` |

Staff (with `BRIGHTSPACE_STAFF=1`):

| Tool | Does | Args |
|---|---|---|
| `brightspace_staff` | Staff overview: per staff course, folders with submitted/graded/unread counts and due dates, ungraded quizzes. | `course` |
| `brightspace_submissions` | One folder: every user/group, submitted at (late marked), files with ids, score, draft/published, rubric result. Filters `status=all\|ungraded\|missing\|late`. | `course`, `assignment` (req), `status`, `who` |
| `brightspace_feedback` | Preview, or with `confirm` save, score + feedback text + rubric levels for a user/group (or `from` a file); draft unless `publish=true`. `publish_all=true` publishes the folder's drafts. | `course`, `assignment` (req), `who`, `score`, `text`, `rubric`, `from`, `publish`, `publish_all`, `confirm` |
| `brightspace_gradebook` | Grade items with stats, or one item's values for everyone, or one student's row (with private comments). | `course` (req), `item`, `who` |
| `brightspace_grade` | Preview / confirm grade values on a grade item (not a folder), one student or `from` a file. | `course`, `item` (req), `who`, `value`, `comment`, `private`, `from`, `confirm` |
| `brightspace_quiz_results` | A quiz's attempts: per student best score, attempts, time taken; or one student's attempts. | `course`, `quiz` (req), `who` |
| `brightspace_announce` | Preview / confirm a news item (title, body as markdown → HTML, optional start date, attachments). | `course` (req), `title` (req), `body` (req), `start`, `files`, `confirm` |
| `brightspace_extension` | Preview / confirm special access (new due/end date) on a folder for a user. | `course`, `assignment` (req), `who` (req), `due`, `end`, `confirm` |
| `brightspace_group_move` | Preview / confirm adding/removing a student to/from a group. | `course`, `category` (req), `who` (req), `to`, `from_group`, `confirm` |

Generic (always on; writes through them still need `confirm`):

| Tool | Does | Args |
|---|---|---|
| `brightspace_api` | Any Valence route. GET returns trimmed JSON; other methods preview, then send with `confirm`. `path=help` lists routes by area. | `method`, `path` (req), `query`, `body`, `max`, `confirm` |
| `brightspace_web` | A site page as text with its forms, or a form POST (preview + `confirm`), for features without an API. | `url` (req), `form`, `fields`, `confirm` |

- `brightspace_sync` gains `assignment`: with it (staff only) it mirrors that folder's
  submissions, one subfolder per user/group.
- `who` takes a user id, a group name (`T3-G 4`), or a name fragment; ambiguous
  matches list candidates.
- `rubric` is `criterion=level` pairs, comma-separated, names or ids
  (`Corrección=Alto, Presentación=Medio`); the score defaults to the rubric total
  when `score` is omitted.

## Check

- Unit tests with the fake server: role gating (`NOT_STAFF`), staff course matching,
  every preview sends nothing, batch file parsing and the 200-row cap, stop at the
  first error, `CONFLICT` on a changed value, the group submit route and its refusal,
  the 404 mapping, `BRIGHTSPACE_STAFF` toggling the tool list.
- Live, read-only: `brightspace_staff`, `_submissions`, `_gradebook`,
  `_quiz_results`, `_people`, `_groups`, `_calendar` against a taught course.
- **No test writes in taught courses.** Write tools are checked
  against the fake server, then live only on real work the user is doing anyway
  (grading a Taller, a real announcement), previewed and confirmed one call at a
  time. The student side gets its live check from the next real group submission
  through `brightspace_submit`.
- `tools/list` stays near 4 KB without staff tools and under 8 KB with them.

## Out of scope

- Nothing the user can do on the site is out of scope for the generic layers.
  Curated tools skip, for now: course design (creating folders, quizzes, grade
  items, content, rubrics), discussions (no current course uses them), taking
  quizzes (timed, interactive; reachable only through `brightspace_web` and not
  recommended), and deletions.

## Open questions

- News create and grade-item/quiz/content edits weren't permission-probed (the probe
  can't tell for them). The first real use answers it.
- Draft semantics: does `IsGraded=false` on the API keep feedback invisible exactly
  like "Guardar borrador" in the UI? Checked on the first real grading: save a
  draft, look at it in the evaluation UI before publishing.
- Does the API download of a submission file flip `IsRead`? On 2026-11-18 a download
  of an already-read file worked and changed nothing
  (`IsRead` true, folder unread 0). An unread file was not tried. Bulk download
  warns that files may be marked read.

## Implementation plan

1. Student fixes (group submit, 404 mapping), with tests. Commit.
1b. `brightspace_api` (with the route catalog) and `brightspace_web`. Commit. From
   here every site action is reachable; the steps below make the common ones cheap.
2. `brightspace_people`, `_groups`, `_calendar`, `_updates`. Commit.
3. Role detection, `BRIGHTSPACE_STAFF`, staff course matching, `NOT_STAFF`; read
   tools `_staff`, `_submissions`, `_gradebook`, `_quiz_results`; `sync assignment=`.
   Commit.
4. Write tools, one commit each after its live check: `_feedback`, `_grade`,
   `_announce`, `_extension`, `_group_move`.
5. `register-claude-desktop.sh`: optional env for the brightspace entry. README
   wording proposed to the user.
