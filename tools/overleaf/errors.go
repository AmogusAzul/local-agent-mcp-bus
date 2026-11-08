package main

import "fmt"

// codeErr is a failure with one of the fixed codes from spec/overleaf.md.
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

const cookieHint = "on overleaf.com open DevTools (F12) → Application → Cookies, copy overleaf_session2 and call overleaf_login with cookie=<value>."

var errAuthExpired = fail("AUTH_EXPIRED", cookieHint, "Overleaf rejected the session cookie (logged out or expired)")
