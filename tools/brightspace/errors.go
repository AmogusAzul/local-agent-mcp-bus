package main

import "fmt"

// codeErr is a failure with one of the fixed codes from spec/brightspace.md.
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
