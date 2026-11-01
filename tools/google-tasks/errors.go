package main

import "fmt"

// codeErr is a failure with one of the fixed codes from spec/google-tasks.md.
// Its text is "CODE: message. Hint: hint."
type codeErr struct {
	code, msg, hint string
}

func (e *codeErr) Error() string {
	s := e.code + ": " + e.msg + "."
	if e.hint != "" {
		s += " Hint: " + e.hint
	}
	return s
}

func fail(code, hint, format string, a ...any) error {
	return &codeErr{code: code, msg: fmt.Sprintf(format, a...), hint: hint}
}

func invalid(format string, a ...any) error {
	return fail("INVALID_ARG", "", format, a...)
}

// Errors for arguments the API has no equivalent for (core.Tool.Reject).
var (
	errDueTime    = (&codeErr{"UNSUPPORTED_DUE_TIME", "Google Tasks can only store a due date, not a time", "pass due as YYYY-MM-DD and mention the time in the title or notes if needed."}).Error()
	errRecurrence = (&codeErr{"UNSUPPORTED_RECURRENCE", "Repeating tasks can't be created or changed through the API", "set the repeat in the Google Tasks app."}).Error()
	errStar       = (&codeErr{"UNSUPPORTED_STAR", "Starring tasks isn't available through the API", ""}).Error()
	errReminder   = (&codeErr{"UNSUPPORTED_REMINDER", "Google Tasks has no reminders apart from the due date", ""}).Error()
)

var unsupportedArgs = map[string]string{
	"star":       errStar,
	"starred":    errStar,
	"repeat":     errRecurrence,
	"recurrence": errRecurrence,
	"reminder":   errReminder,
	"time":       errDueTime,
	"due_time":   errDueTime,
}
