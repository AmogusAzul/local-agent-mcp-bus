package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const envCookie = "OVERLEAF_SESSION"

// accounts is the on-disk cookie store: one overleaf_session2 per account email.
type accounts struct {
	Active  string            `json:"active"`
	Cookies map[string]string `json:"cookies"`
}

func loadAccounts(path string) (*accounts, error) {
	a := &accounts{Cookies: map[string]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, a); err != nil {
		return nil, err
	}
	if a.Cookies == nil {
		a.Cookies = map[string]string{}
	}
	return a, nil
}

func (a *accounts) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// cookie returns the account's cookie ("" means the active one). Without stored
// accounts, OVERLEAF_SESSION from the environment is used under the name "env".
func (a *accounts) cookie(account string) (string, string, error) {
	if account == "" {
		account = a.Active
	}
	if account == "env" || account == "" && len(a.Cookies) == 0 {
		if c := cleanCookie(os.Getenv(envCookie)); c != "" {
			return "env", c, nil
		}
	}
	if account == "" || account == "env" {
		return "", "", fail("NOT_LOGGED_IN", cookieHint, "no Overleaf account is logged in")
	}
	c, ok := a.Cookies[account]
	if !ok {
		return "", "", fail("NOT_LOGGED_IN", cookieHint, "account %s is not logged in", account)
	}
	return account, c, nil
}

func (a *accounts) names() []string {
	var out []string
	for k := range a.Cookies {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cleanCookie accepts the bare value or "overleaf_session2=<value>".
func cleanCookie(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "overleaf_session2=")
	s = strings.TrimSuffix(s, ";")
	return strings.TrimSpace(s)
}
