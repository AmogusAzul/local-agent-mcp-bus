package bs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// routeHelp is the compact Valence route catalog brightspace_api answers to
// path=help. {O} is a course (org unit) id; versions are filled in.
const routeHelp = `Valence routes (prefix le/ or lp/; {O} course id, versions are added). GET reads; anything else previews, then needs confirm=true.
Courses/people: lp/enrollments/myenrollments/ | lp/courses/{O} | le/{O}/classlist/ | lp/enrollments/orgUnits/{O}/users/ | lp/users/whoami
Groups: lp/{O}/groupcategories/ | .../{cat}/groups/ | POST .../{cat}/groups/{g}/enrollments/ {"UserId":n} | DELETE .../enrollments/{userId}
Assignments: le/{O}/dropbox/folders/ | .../folders/{f} | .../{f}/submissions/ (?activeOnly=true) | .../{f}/submissions/mysubmissions/ | .../{f}/submissions/{s}/files/{file}
 feedback: GET|POST le/{O}/dropbox/folders/{f}/feedback/{user|group}/{id} {"Score":n,"Feedback":{"Content":"","Type":"Html"},"RubricAssessments":[],"IsGraded":false,"GradedSymbol":null}
 special access: GET .../{f}/specialaccess/ | PUT .../{f}/specialaccess/{userId} {"StartDate":null,"EndDate":null,"DueDate":"..."}
Rubrics: le/{O}/rubrics?objectType=Dropbox&objectId={f} | le/{O}/assessment?assessmentType=Rubric&objectType=Dropbox&objectId={f}&rubricId={r}&userId={u} (GET/PUT)
Grades: le/{O}/grades/ | .../grades/{go} | .../{go}/values/ | .../{go}/statistics | .../grades/values/{userId}/ | .../grades/values/myGradeValues/ | .../grades/final/values/
 PUT le/{O}/grades/{go}/values/{userId} {"GradeObjectType":1,"PointsNumerator":n,"Comments":{"Content":"","Type":"Text"},"PrivateComments":{"Content":"","Type":"Text"}}
Quizzes: le/{O}/quizzes/ | .../quizzes/{q} | .../{q}/attempts/ (?userId=) | .../{q}/attempts/{a} | .../{q}/specialaccess/
News: le/{O}/news/ (?showDeleted=true) | POST le/{O}/news/ | PUT|DELETE le/{O}/news/{id}
Calendar: le/calendar/events/myEvents/?orgUnitIdsCSV=a,b&startDateTime=..&endDateTime=.. | le/{O}/calendar/events/ | POST le/{O}/calendar/event/
Content: le/{O}/content/toc | .../content/root/ | .../content/modules/{m}/structure/ | .../content/topics/{t} | .../topics/{t}/file
Other: le/{O}/updates/myUpdates | lp/{O}/managefiles/ | le/lti/link/{O}/ | le/{O}/discussions/forums/
No API (use brightspace_web): attendance, email, quiz question grading.`

var versionSeg = regexp.MustCompile(`^\d+\.\d+$`)

// apiPath turns a route like "le/12345/news/" into a full API path with the
// product's version. A version already present, or "{v}", is kept or filled.
func (a *api) apiPath(ctx context.Context, route string) (string, error) {
	route = strings.TrimSpace(route)
	route = strings.TrimPrefix(route, "/")
	route = strings.TrimPrefix(route, "d2l/api/")
	if strings.Contains(route, "://") || strings.Contains(route, "..") {
		return "", invalid("path is a route under /d2l/api/, like le/{O}/news/")
	}
	parts := strings.SplitN(route, "/", 3)
	if len(parts) < 2 || parts[0] == "" {
		return "", invalid("path must start with a product, like le/ or lp/ (path=help lists routes)")
	}
	if _, _, err := a.versions(ctx); err != nil {
		return "", err
	}
	a.mu.Lock()
	v := a.vers[parts[0]]
	a.mu.Unlock()
	if v == "" {
		return "", invalid("unknown API product %q", parts[0])
	}
	rest := strings.Join(parts[1:], "/")
	switch {
	case parts[1] == "{v}":
		rest = strings.Join(parts[2:], "/")
	case versionSeg.MatchString(parts[1]):
		return "/d2l/api/" + route, nil
	}
	return "/d2l/api/" + parts[0] + "/" + v + "/" + rest, nil
}

// trimJSON cuts long strings and arrays so a reply stays readable.
func trimJSON(v any) any {
	switch x := v.(type) {
	case string:
		return trunc(x, 300)
	case []any:
		n := len(x)
		if n > 50 {
			x = append(x[:50:50], fmt.Sprintf("… %d more", n-50))
		}
		for i := range x {
			x[i] = trimJSON(x[i])
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = trimJSON(e)
		}
		return x
	}
	return v
}

// replyText renders a reply body compactly: trimmed JSON, plain text from
// HTML, or a note for binary content.
func replyText(resp *http.Response, max int) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	ct := resp.Header.Get("Content-Type")
	var out string
	switch {
	case len(bytes.TrimSpace(b)) == 0:
		out = "(empty reply)"
	case strings.Contains(ct, "json") || json.Valid(b):
		var v any
		if json.Unmarshal(b, &v) == nil {
			js, _ := json.Marshal(trimJSON(v))
			out = string(js)
		} else {
			out = string(b)
		}
	case strings.HasPrefix(ct, "text/") || strings.Contains(ct, "html") || strings.Contains(ct, "xml"):
		out = plain(string(b))
	default:
		return fmt.Sprintf("(binary reply, %s, %s; use brightspace_download for files)", ct, size(int64(len(b))))
	}
	return trunc(out, max)
}

func maxArg(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 200 || n > 50000 {
		return 0, invalid("max must be a number from 200 to 50000")
	}
	return n, nil
}

// bodyArg reads a body given inline or as @path.
func bodyArg(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "@") {
		b, err := os.ReadFile(strings.TrimSpace(s[1:]))
		if err != nil {
			return nil, invalid("can't read body file: %v", err)
		}
		return b, nil
	}
	return []byte(s), nil
}

func (a *api) toolAPI(ctx context.Context, args map[string]string) (string, error) {
	if strings.TrimSpace(args["path"]) == "help" {
		return routeHelp, nil
	}
	method := strings.ToUpper(strings.TrimSpace(args["method"]))
	if method == "" {
		method = "GET"
	}
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH":
	default:
		return "", invalid("method must be GET, POST, PUT, DELETE or PATCH")
	}
	max, err := maxArg(args["max"], 6000)
	if err != nil {
		return "", err
	}
	path, err := a.apiPath(ctx, args["path"])
	if err != nil {
		return "", err
	}
	if q := strings.TrimPrefix(strings.TrimSpace(args["query"]), "?"); q != "" {
		if _, err := url.ParseQuery(q); err != nil {
			return "", invalid("query must be like a=1&b=2")
		}
		if strings.Contains(path, "?") {
			path += "&" + q
		} else {
			path += "?" + q
		}
	}
	body, err := bodyArg(args["body"])
	if err != nil {
		return "", err
	}
	if len(body) > 0 && method == "GET" {
		return "", invalid("GET takes no body")
	}
	if len(body) > 0 && !json.Valid(body) {
		return "", invalid("body must be JSON")
	}
	if method != "GET" {
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s\n", method, path)
		if len(body) > 0 {
			fmt.Fprintf(&b, "Body: %s\n", trunc(string(body), 2000))
		}
		if args["confirm"] != "true" {
			b.WriteString("\nNothing was sent. This acts as the user on the live site. Show it to the user and, once they agree, call again with confirm=true.")
			return b.String(), nil
		}
		if err := a.approved(ctx, b.String()); err != nil {
			return "", err
		}
	}
	resp, err := a.send(ctx, func(string) (*http.Request, error) {
		var rd io.Reader
		if len(body) > 0 {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, a.site+path, rd)
		if err != nil {
			return nil, err
		}
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", statusErr(resp, method+" "+path)
	}
	text := replyText(resp, max)
	if method == "GET" {
		return text, nil
	}
	return fmt.Sprintf("%s %s: HTTP %d\n%s", method, path, resp.StatusCode, text), nil
}

// webForm is a form found on a page.
type webForm struct {
	name, id, method, action string
	fields                   []webField
}

type webField struct {
	name, value, kind string
	options           []string // select options as value:label
}

var (
	formRe     = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	inputRe    = regexp.MustCompile(`(?is)<input\b([^>]*)>`)
	selectRe   = regexp.MustCompile(`(?is)<select\b([^>]*)>(.*?)</select>`)
	optionRe   = regexp.MustCompile(`(?is)<option\b([^>]*)>(.*?)</option>`)
	textareaRe = regexp.MustCompile(`(?is)<textarea\b([^>]*)>(.*?)</textarea>`)
	attrRe     = regexp.MustCompile(`(?is)([a-z_:][-a-z0-9_:.]*)\s*(?:=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?`)
	titleRe    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	linkRe     = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
)

func attrs(s string) map[string]string {
	m := map[string]string{}
	for _, x := range attrRe.FindAllStringSubmatch(s, -1) {
		k := strings.ToLower(x[1])
		if _, ok := m[k]; ok {
			continue
		}
		m[k] = htmlUnescape(x[2] + x[3] + x[4])
	}
	return m
}

func htmlUnescape(s string) string { return strings.TrimSpace(plainAttr(s)) }

// plainAttr unescapes entities without touching layout.
func plainAttr(s string) string {
	return strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">", "&#x2F;", "/").Replace(s)
}

func parseForms(page string) []webForm {
	var out []webForm
	for _, m := range formRe.FindAllStringSubmatch(page, -1) {
		at := attrs(m[1])
		f := webForm{name: at["name"], id: at["id"], method: strings.ToUpper(at["method"]), action: at["action"]}
		if f.method == "" {
			f.method = "GET"
		}
		inner := m[2]
		for _, in := range inputRe.FindAllStringSubmatch(inner, -1) {
			ia := attrs(in[1])
			if ia["name"] == "" {
				continue
			}
			kind := strings.ToLower(ia["type"])
			if kind == "" {
				kind = "text"
			}
			if (kind == "checkbox" || kind == "radio") && !strings.Contains(strings.ToLower(in[1]), "checked") {
				f.fields = append(f.fields, webField{name: ia["name"], kind: kind + " (unchecked, value " + ia["value"] + ")"})
				continue
			}
			if kind == "submit" || kind == "button" || kind == "image" || kind == "reset" {
				continue
			}
			f.fields = append(f.fields, webField{name: ia["name"], value: ia["value"], kind: kind})
		}
		for _, sel := range selectRe.FindAllStringSubmatch(inner, -1) {
			sa := attrs(sel[1])
			if sa["name"] == "" {
				continue
			}
			wf := webField{name: sa["name"], kind: "select"}
			for i, op := range optionRe.FindAllStringSubmatch(sel[2], -1) {
				oa := attrs(op[1])
				v, ok := oa["value"]
				if !ok {
					v = plain(op[2])
				}
				if i == 0 || strings.Contains(strings.ToLower(op[1]), "selected") {
					wf.value = v
				}
				wf.options = append(wf.options, v+":"+trunc(plain(op[2]), 30))
			}
			f.fields = append(f.fields, wf)
		}
		for _, ta := range textareaRe.FindAllStringSubmatch(inner, -1) {
			tt := attrs(ta[1])
			if tt["name"] != "" {
				f.fields = append(f.fields, webField{name: tt["name"], value: plainAttr(ta[2]), kind: "textarea"})
			}
		}
		out = append(out, f)
	}
	return out
}

// value tells whether a field is sent as-is (checked or not a checkbox).
func (f webField) sent() bool { return !strings.Contains(f.kind, "unchecked") }

func formText(i int, f webForm) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Form %d", i+1)
	if f.name != "" {
		fmt.Fprintf(&b, " name=%s", f.name)
	}
	if f.id != "" {
		fmt.Fprintf(&b, " id=%s", f.id)
	}
	fmt.Fprintf(&b, " %s %s\n", f.method, f.action)
	for _, fl := range f.fields {
		v := fl.value
		if fl.kind == "hidden" {
			v = trunc(v, 40)
		} else {
			v = trunc(v, 120)
		}
		fmt.Fprintf(&b, "  %s=%s (%s)", fl.name, v, fl.kind)
		if len(fl.options) > 0 {
			ops := fl.options
			if len(ops) > 15 {
				ops = append(ops[:15:15], "…")
			}
			b.WriteString(" options: " + strings.Join(ops, ", "))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// webURL resolves a page address on the site; other hosts are refused.
func (a *api) webURL(raw, base string) (string, error) {
	site, _ := url.Parse(a.site)
	b, err := url.Parse(base)
	if err != nil || base == "" {
		b = site
	}
	u, err := b.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", invalid("bad url %q", raw)
	}
	if u.Host != site.Host || (u.Scheme != "http" && u.Scheme != "https") {
		return "", invalid("url must be on %s", site.Host)
	}
	u.Scheme = site.Scheme
	return u.String(), nil
}

var webClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// webDo sends a request with the site cookies, following redirects on the
// site. A redirect to the login page signs in again and retries once.
func (a *api) webDo(ctx context.Context, method, target string, form url.Values) (string, *http.Response, error) {
	for attempt := 0; ; attempt++ {
		cookie, xsrf, err := a.s.cookies(ctx)
		if err != nil {
			return "", nil, err
		}
		cur, m, data := target, method, form
		for hop := 0; ; hop++ {
			var rd io.Reader
			if m == "POST" {
				rd = strings.NewReader(data.Encode())
			}
			req, err := http.NewRequestWithContext(ctx, m, cur, rd)
			if err != nil {
				return "", nil, err
			}
			req.Header.Set("Cookie", cookie)
			if m == "POST" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("X-Csrf-Token", xsrf)
			}
			resp, err := webClient.Do(req)
			if err != nil {
				return "", nil, fail("API_ERROR", "check the network and retry.", "Couldn't reach the site: %v", err)
			}
			loc := resp.Header.Get("Location")
			if resp.StatusCode/100 != 3 || loc == "" {
				return cur, resp, nil
			}
			resp.Body.Close()
			if strings.Contains(loc, "/d2l/login") || strings.Contains(loc, "sessionExpired") {
				if attempt > 0 {
					return "", nil, fail("API_ERROR", "retry to sign in again.", "The site keeps asking for a login")
				}
				a.s.expire(cookie)
				break
			}
			next, err := a.webURL(loc, cur)
			if err != nil || hop == 5 {
				return "", nil, fail("API_ERROR", "", "The page redirected off the site or in a loop (%s)", loc)
			}
			cur, m, data = next, "GET", nil
		}
	}
}

func (a *api) toolWeb(ctx context.Context, args map[string]string) (string, error) {
	max, err := maxArg(args["max"], 6000)
	if err != nil {
		return "", err
	}
	target, err := a.webURL(args["url"], "")
	if err != nil {
		return "", err
	}
	formArg, fieldsArg := strings.TrimSpace(args["form"]), strings.TrimSpace(args["fields"])
	if formArg == "" && fieldsArg == "" {
		final, resp, err := a.webDo(ctx, "GET", target, nil)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return "", statusErr(resp, "The page")
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return pageText(final, string(b), max), nil
	}

	fields, err := parseFields(fieldsArg)
	if err != nil {
		return "", err
	}
	method, action := "POST", target
	values := url.Values{}
	if formArg != "" {
		final, resp, err := a.webDo(ctx, "GET", target, nil)
		if err != nil {
			return "", err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return "", fail("API_ERROR", "", "The page answered HTTP %d", resp.StatusCode)
		}
		forms := parseForms(string(b))
		f, err := pickForm(forms, formArg)
		if err != nil {
			return "", err
		}
		for _, fl := range f.fields {
			if fl.kind == "file" {
				return "", fail("UNSUPPORTED_FOLDER", "do it on the site.", "This form uploads a file, which brightspace_web can't send")
			}
			if fl.sent() {
				values.Add(fl.name, fl.value)
			}
		}
		method = f.method
		if action, err = a.webURL(f.action, final); err != nil {
			return "", err
		}
	} else {
		_, xsrf, err := a.s.cookies(ctx)
		if err != nil {
			return "", err
		}
		values.Set("d2l_referrer", xsrf)
	}
	for k, v := range fields {
		values.Set(k, v)
	}
	if method == "GET" {
		u, _ := url.Parse(action)
		q := u.Query()
		for k, vs := range values {
			q[k] = vs
		}
		u.RawQuery = q.Encode()
		action = u.String()
	}
	var pv strings.Builder
	fmt.Fprintf(&pv, "%s %s\n", method, action)
	for k, vs := range values {
		for _, v := range vs {
			if strings.HasPrefix(k, "d2l_") {
				v = trunc(v, 20)
			}
			fmt.Fprintf(&pv, "  %s=%s\n", k, trunc(v, 300))
		}
	}
	if args["confirm"] != "true" {
		pv.WriteString("\nNothing was sent. This acts as the user on the live site. Show it to the user and, once they agree, call again with confirm=true.")
		return pv.String(), nil
	}
	if err := a.approved(ctx, pv.String()); err != nil {
		return "", err
	}
	var form url.Values
	if method == "POST" {
		form = values
	}
	final, resp, err := a.webDo(ctx, method, action, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return fmt.Sprintf("%s %s: HTTP %d\n%s", method, action, resp.StatusCode, pageText(final, string(b), max)), nil
}

func pickForm(forms []webForm, arg string) (webForm, error) {
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(forms) {
			return webForm{}, invalid("the page has %d form(s)", len(forms))
		}
		return forms[n-1], nil
	}
	for _, f := range forms {
		if f.name == arg || f.id == arg {
			return f, nil
		}
	}
	return webForm{}, invalid("no form named %q (pass its number from the page listing)", arg)
}

// parseFields reads fields as a JSON object or a=1&b=2.
func parseFields(s string) (map[string]string, error) {
	out := map[string]string{}
	if s == "" {
		return out, nil
	}
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, invalid("fields JSON: %v", err)
		}
		for k, v := range m {
			switch x := v.(type) {
			case string:
				out[k] = x
			default:
				js, _ := json.Marshal(x)
				out[k] = string(js)
			}
		}
		return out, nil
	}
	q, err := url.ParseQuery(s)
	if err != nil {
		return nil, invalid("fields must be a JSON object or a=1&b=2")
	}
	for k := range q {
		out[k] = q.Get(k)
	}
	return out, nil
}

// pageText is a page as compact text, its forms and its links.
func pageText(addr, page string, max int) string {
	var b strings.Builder
	if m := titleRe.FindStringSubmatch(page); m != nil {
		fmt.Fprintf(&b, "Title: %s\n", plain(m[1]))
	}
	fmt.Fprintf(&b, "URL: %s\n", addr)
	forms := parseForms(page)
	for i, f := range forms {
		b.WriteString(formText(i, f))
	}
	var links []string
	seen := map[string]bool{}
	for _, m := range linkRe.FindAllStringSubmatch(page, -1) {
		href := attrs(m[1])["href"]
		text := trunc(plain(m[2]), 50)
		if href == "" || text == "" || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "#") || seen[href] {
			continue
		}
		seen[href] = true
		links = append(links, text+" → "+href)
		if len(links) == 60 {
			links = append(links, "…")
			break
		}
	}
	if len(links) > 0 {
		b.WriteString("Links: " + strings.Join(links, " | ") + "\n")
	}
	b.WriteString("Text:\n" + plain(page))
	return trunc(b.String(), max)
}
