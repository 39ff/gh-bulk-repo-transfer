package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fake GitHub API -------------------------------------------------------

type fakeGitHub struct {
	mu               sync.Mutex
	login            string
	accounts         map[string]Account // lower(login) -> account
	repos            map[string]*Repo   // lower(full name) -> repo
	redirects        map[string]string  // lower(old full name) -> new full name
	transfers        []string
	archived         []string
	orgs             []string // organizations the user belongs to
	ownerOf          map[string]bool
	noMembershipsAPI bool // simulate a token without read:org
	perPage          int
	// rateLimitNextTransfer makes the next transfer fail once with a secondary rate limit.
	rateLimitNextTransfer bool
	// archive404 makes PATCH return 404 this many times (transfer still in progress).
	archive404 int
}

func newFakeGitHub() *fakeGitHub {
	f := &fakeGitHub{
		login:     "alice",
		accounts:  map[string]Account{},
		repos:     map[string]*Repo{},
		redirects: map[string]string{},
		perPage:   2,
	}
	f.addAccount("alice", "User")
	f.addAccount("archive-org", "Organization")
	f.addAccount("bob", "User")
	f.addAccount("other-org", "Organization")
	f.orgs = []string{"archive-org", "other-org"}
	f.ownerOf = map[string]bool{"archive-org": true}
	return f
}

func (f *fakeGitHub) addAccount(login, typ string) {
	f.accounts[strings.ToLower(login)] = Account{Login: login, Type: typ}
}

func (f *fakeGitHub) addRepo(owner, name string, mod func(*Repo)) {
	t := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &Repo{
		Name: name, FullName: owner + "/" + name, Owner: f.accounts[strings.ToLower(owner)],
		Visibility: "public", PushedAt: &t, Permissions: &Permissions{Admin: true},
	}
	if mod != nil {
		mod(r)
	}
	f.repos[strings.ToLower(r.FullName)] = r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, 404, map[string]string{"message": "Not Found"})
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-token" {
		writeJSON(w, 401, map[string]string{"message": "Bad credentials"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == "GET" && r.URL.Path == "/user":
		writeJSON(w, 200, f.accounts[f.login])
	case r.Method == "GET" && r.URL.Path == "/user/repos":
		f.paginate(w, r, f.login)
	case r.Method == "GET" && r.URL.Path == "/user/memberships/orgs":
		if f.noMembershipsAPI {
			writeJSON(w, 403, map[string]string{"message": "Resource not accessible"})
			return
		}
		var list []map[string]any
		for _, o := range f.orgs {
			role := "member"
			if f.ownerOf[o] {
				role = "admin"
			}
			list = append(list, map[string]any{"role": role, "state": "active", "organization": Account{Login: o}})
		}
		writeJSON(w, 200, list)
	case r.Method == "GET" && r.URL.Path == "/user/orgs":
		var list []Account
		for _, o := range f.orgs {
			list = append(list, Account{Login: o})
		}
		writeJSON(w, 200, list)
	case r.Method == "GET" && len(parts) == 2 && parts[0] == "users":
		if a, ok := f.accounts[strings.ToLower(parts[1])]; ok {
			writeJSON(w, 200, a)
		} else {
			notFound(w)
		}
	case r.Method == "GET" && len(parts) == 3 && parts[0] == "orgs" && parts[2] == "repos":
		f.paginate(w, r, parts[1])
	case len(parts) >= 3 && parts[0] == "repos":
		full := strings.ToLower(parts[1] + "/" + parts[2])
		repo, ok := f.repos[full]
		if !ok {
			if to, moved := f.redirects[full]; moved && r.Method == "GET" {
				http.Redirect(w, r, "/repos/"+to, http.StatusMovedPermanently)
				return
			}
			notFound(w)
			return
		}
		switch {
		case r.Method == "GET" && len(parts) == 3:
			writeJSON(w, 200, repo)
		case r.Method == "PATCH" && len(parts) == 3:
			if f.archive404 > 0 {
				f.archive404--
				notFound(w)
				return
			}
			repo.Archived = true
			f.archived = append(f.archived, repo.FullName)
			writeJSON(w, 200, repo)
		case r.Method == "POST" && len(parts) == 4 && parts[3] == "transfer":
			f.transfer(w, r, repo)
		default:
			notFound(w)
		}
	default:
		notFound(w)
	}
}

func (f *fakeGitHub) transfer(w http.ResponseWriter, r *http.Request, repo *Repo) {
	if f.rateLimitNextTransfer {
		f.rateLimitNextTransfer = false
		w.Header().Set("Retry-After", "30")
		writeJSON(w, 403, map[string]string{"message": "You have exceeded a secondary rate limit."})
		return
	}
	var body struct {
		NewOwner string `json:"new_owner"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"message": "Problems parsing JSON"})
		return
	}
	dest, ok := f.accounts[strings.ToLower(body.NewOwner)]
	if !ok {
		writeJSON(w, 422, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"message": "new_owner not found"}}})
		return
	}
	newFull := dest.Login + "/" + repo.Name
	if _, exists := f.repos[strings.ToLower(newFull)]; exists {
		writeJSON(w, 422, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"message": "name already exists on this account"}}})
		return
	}
	oldFull := repo.FullName
	delete(f.repos, strings.ToLower(oldFull))
	repo.Owner, repo.FullName = dest, newFull
	f.repos[strings.ToLower(newFull)] = repo
	f.redirects[strings.ToLower(oldFull)] = newFull
	f.transfers = append(f.transfers, oldFull+"->"+dest.Login)
	writeJSON(w, 202, repo)
}

func (f *fakeGitHub) paginate(w http.ResponseWriter, r *http.Request, owner string) {
	var list []*Repo
	for _, repo := range f.repos {
		if strings.EqualFold(repo.Owner.Login, owner) {
			list = append(list, repo)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].FullName < list[j].FullName })
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start := min((page-1)*f.perPage, len(list))
	end := min(start+f.perPage, len(list))
	if end < len(list) {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(page+1))
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?%s>; rel="next", <http://%s/last>; rel="last"`, r.Host, r.URL.Path, q.Encode(), r.Host))
	}
	writeJSON(w, 200, list[start:end])
}

// ---- test helpers -----------------------------------------------------------

type result struct {
	code           int
	stdout, stderr string
	sleeps         []time.Duration
}

func runCLI(t *testing.T, srv *httptest.Server, stdin string, args ...string) result {
	t.Helper()
	var sleeps []time.Duration
	origSleep, origEnv, origGH := sleepFn, getenv, ghAuthToken
	sleepFn = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return ctx.Err()
	}
	getenv = func(string) string { return "" }
	ghAuthToken = func(context.Context, string) (string, error) { return "", errors.New("no gh") }
	t.Cleanup(func() { sleepFn, getenv, ghAuthToken = origSleep, origEnv, origGH })

	if srv != nil {
		args = append(args, "--api-url", srv.URL, "--token", "test-token")
	}
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, newLineReader(stdin), &out, &errOut)
	return result{code: code, stdout: out.String(), stderr: errOut.String(), sleeps: sleeps}
}

// lineReader returns one line per Read call, like a terminal in line mode.
// (huh's plain-text prompts create a new scanner for every question.)
type lineReader struct{ lines []string }

func newLineReader(s string) *lineReader {
	if s == "" {
		return &lineReader{}
	}
	return &lineReader{lines: strings.SplitAfter(s, "\n")}
}

func (l *lineReader) Read(p []byte) (int, error) {
	for len(l.lines) > 0 && l.lines[0] == "" {
		l.lines = l.lines[1:]
	}
	if len(l.lines) == 0 {
		return 0, io.EOF
	}
	n := copy(p, l.lines[0])
	l.lines[0] = l.lines[0][n:]
	return n, nil
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "repos.txt")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func standardFake() (*fakeGitHub, *httptest.Server) {
	f := newFakeGitHub()
	old := time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)
	f.addRepo("alice", "foo", nil)
	f.addRepo("alice", "bar", func(r *Repo) { r.Visibility, r.Private = "private", true })
	f.addRepo("alice", "baz", nil)
	f.addRepo("alice", "keep-me", nil)
	f.addRepo("alice", "old-fork", func(r *Repo) { r.Fork, r.PushedAt = true, &old })
	f.addRepo("alice", "old-thing", func(r *Repo) { r.PushedAt = &old })
	f.addRepo("alice", "empty", func(r *Repo) { r.PushedAt = nil })
	f.addRepo("archive-org", "baz", nil) // name conflict
	f.addRepo("bob", "shared", func(r *Repo) { r.Permissions = &Permissions{Admin: false} })
	return f, httptest.NewServer(f)
}

// ---- end-to-end tests -------------------------------------------------------

func TestTransferFromFile(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	f.rateLimitNextTransfer = true
	f.archive404 = 1

	file := writeFile(t, "\uFEFF# header comment\n"+
		"foo\n"+
		"alice/bar   # private  pushed:2024-01-01\n"+
		"https://github.com/alice/baz\n"+
		"\n"+
		"#alice/keep-me\n"+
		"missing\n"+
		"bob/shared\n"+
		"alice/foo\n") // duplicate

	res := runCLI(t, srv, "", "transfer", "--to", "archive-org", "--file", file, "--yes", "--archive", "--interval", "5s")
	if res.code != 1 {
		t.Fatalf("exit code = %d, want 1 (errors in plan)\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	wantTransfers := []string{"alice/foo->archive-org", "alice/bar->archive-org"}
	if fmt.Sprint(f.transfers) != fmt.Sprint(wantTransfers) {
		t.Errorf("transfers = %v, want %v", f.transfers, wantTransfers)
	}
	wantArchived := []string{"archive-org/foo", "archive-org/bar"}
	if fmt.Sprint(f.archived) != fmt.Sprint(wantArchived) {
		t.Errorf("archived = %v, want %v", f.archived, wantArchived)
	}
	for _, want := range []string{
		"skip      alice/baz",
		"archive-org/baz already exists",
		"error     alice/missing",
		"not found",
		"error     bob/shared",
		"admin permission",
		"2 to transfer, 1 skipped, 2 error(s)",
		"Done: 2 transferred, 0 failed",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout missing %q\n%s", want, res.stdout)
		}
	}
	if strings.Contains(res.stdout, "keep-me") {
		t.Errorf("commented-out repository should not appear:\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr, "rate limited") {
		t.Errorf("stderr should mention rate limiting:\n%s", res.stderr)
	}
	// rate-limit wait (30s), interval between transfers (5s), archive retry (3s)
	want := map[time.Duration]bool{30 * time.Second: false, 5 * time.Second: false, 3 * time.Second: false}
	for _, d := range res.sleeps {
		if _, ok := want[d]; ok {
			want[d] = true
		}
	}
	for d, seen := range want {
		if !seen {
			t.Errorf("expected a %s wait, got sleeps %v", d, res.sleeps)
		}
	}
}

func TestTransferDryRun(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	res := runCLI(t, srv, "", "transfer", "foo", "bar", "--to", "archive-org", "--dry-run")
	if res.code != 0 {
		t.Fatalf("exit code = %d\n%s%s", res.code, res.stdout, res.stderr)
	}
	if len(f.transfers) != 0 {
		t.Errorf("dry run transferred %v", f.transfers)
	}
	if !strings.Contains(res.stdout, "Dry run") || !strings.Contains(res.stdout, "2 to transfer") {
		t.Errorf("unexpected output:\n%s", res.stdout)
	}
}

func TestTransferWithFiltersAndConfirmation(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	res := runCLI(t, srv, "y\n", "transfer", "--to", "archive-org", "--pushed-before", "2023-01-01", "--forks", "exclude", "--interval", "0")
	if res.code != 0 {
		t.Fatalf("exit code = %d\n%s%s", res.code, res.stdout, res.stderr)
	}
	// old-thing (old) and empty (never pushed) match; old-fork is a fork.
	want := []string{"alice/empty->archive-org", "alice/old-thing->archive-org"}
	if fmt.Sprint(f.transfers) != fmt.Sprint(want) {
		t.Errorf("transfers = %v, want %v", f.transfers, want)
	}
	if !strings.Contains(res.stdout, "Transfer 2 repositories to archive-org? [y/N]") {
		t.Errorf("missing confirmation prompt:\n%s", res.stdout)
	}
}

func TestTransferAborted(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	res := runCLI(t, srv, "n\n", "transfer", "--to", "archive-org", "foo")
	if res.code != 1 || len(f.transfers) != 0 {
		t.Fatalf("code=%d transfers=%v\n%s", res.code, f.transfers, res.stdout)
	}
	if !strings.Contains(res.stdout, "Aborted") {
		t.Errorf("missing abort message:\n%s", res.stdout)
	}
}

func TestTransferAlreadyMovedIsSkipped(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	f.addRepo("archive-org", "moved", nil)
	f.redirects["alice/moved"] = "archive-org/moved"
	res := runCLI(t, srv, "", "transfer", "--to", "archive-org", "alice/moved", "--yes")
	if res.code != 0 || len(f.transfers) != 0 {
		t.Fatalf("code=%d transfers=%v\n%s%s", res.code, f.transfers, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "already owned by archive-org") {
		t.Errorf("unexpected output:\n%s", res.stdout)
	}
}

func TestTransferUsageErrors(t *testing.T) {
	cases := [][]string{
		{"transfer", "foo"},                                 // no --to
		{"transfer", "--to", "org"},                         // nothing selected
		{"transfer", "--to", "org", "--all", "foo"},         // --all with list
		{"transfer", "--to", "org", "--file", "-"},          // stdin without --yes
		{"transfer", "--to", "org", "--forks", "sometimes"}, // bad filter
	}
	for _, args := range cases {
		res := runCLI(t, nil, "", args...)
		if res.code != 2 {
			t.Errorf("%v: exit code = %d, want 2\n%s", args, res.code, res.stderr)
		}
	}
}

func TestTransferUnknownDestination(t *testing.T) {
	_, srv := standardFake()
	defer srv.Close()
	res := runCLI(t, srv, "", "transfer", "--to", "nobody", "foo", "--yes")
	if res.code != 1 || !strings.Contains(res.stderr, `destination "nobody" does not exist`) {
		t.Errorf("code=%d stderr=%s", res.code, res.stderr)
	}
}

func TestBadToken(t *testing.T) {
	_, srv := standardFake()
	defer srv.Close()
	var out, errOut bytes.Buffer
	origEnv, origGH := getenv, ghAuthToken
	defer func() { getenv, ghAuthToken = origEnv, origGH }()
	getenv = func(string) string { return "" }
	code := run(context.Background(), []string{"list", "--api-url", srv.URL, "--token", "wrong"}, strings.NewReader(""), &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "Bad credentials") {
		t.Errorf("code=%d stderr=%s", code, errOut.String())
	}
}

func TestListText(t *testing.T) {
	_, srv := standardFake()
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "list.txt")
	res := runCLI(t, srv, "", "list", "--sort", "pushed", "-o", out)
	if res.code != 0 {
		t.Fatalf("exit code = %d\n%s", res.code, res.stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := readRepoList(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	// pagination (2 per page) must return all 7; sorted never-pushed first, then oldest.
	want := []string{"alice/empty", "alice/old-fork", "alice/old-thing", "alice/bar", "alice/baz", "alice/foo", "alice/keep-me"}
	if fmt.Sprint(refs) != fmt.Sprint(want) {
		t.Errorf("refs = %v\nwant  %v\nfile:\n%s", refs, want, data)
	}
	for _, s := range []string{"private", "fork", "pushed:never", "pushed:2020-05-01"} {
		if !bytes.Contains(data, []byte(s)) {
			t.Errorf("list file missing %q:\n%s", s, data)
		}
	}
}

func TestListJSONFromOrgWithFilter(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	f.addRepo("archive-org", "old-archived", func(r *Repo) { r.Archived = true })
	res := runCLI(t, srv, "", "list", "--from", "archive-org", "--format", "json", "--archived", "only")
	if res.code != 0 {
		t.Fatalf("exit code = %d\n%s", res.code, res.stderr)
	}
	var items []repoJSON
	if err := json.Unmarshal([]byte(res.stdout), &items); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, res.stdout)
	}
	if len(items) != 1 || items[0].FullName != "archive-org/old-archived" || !items[0].Archived {
		t.Errorf("items = %+v", items)
	}
}

func TestVersion(t *testing.T) {
	res := runCLI(t, nil, "", "version")
	if res.code != 0 || !strings.HasPrefix(res.stdout, "ghbt ") {
		t.Errorf("code=%d out=%q", res.code, res.stdout)
	}
}

// ---- unit tests -------------------------------------------------------------

func TestParseRepoRef(t *testing.T) {
	cases := []struct {
		in, owner, name string
		wantErr         bool
	}{
		{"foo", "me", "foo", false},
		{"alice/foo", "alice", "foo", false},
		{"https://github.com/alice/foo", "alice", "foo", false},
		{"https://github.com/alice/foo.git", "alice", "foo", false},
		{"https://github.com/alice/foo/tree/main", "alice", "foo", false},
		{"git@github.com:alice/foo.git", "alice", "foo", false},
		{"alice/foo/bar", "", "", true},
		{"alice/", "", "", true},
		{"../etc", "", "", true},
		{"a b", "", "", true},
	}
	for _, c := range cases {
		owner, name, err := parseRepoRef(c.in, "me")
		if (err != nil) != c.wantErr || owner != c.owner || name != c.name {
			t.Errorf("parseRepoRef(%q) = %q, %q, %v", c.in, owner, name, err)
		}
	}
}

func TestReadRepoList(t *testing.T) {
	in := "\uFEFFalice/a  # comment\r\n\n  # full comment\nb c\r\nd#e\n"
	refs, err := readRepoList(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alice/a", "b", "c", "d"}; fmt.Sprint(refs) != fmt.Sprint(want) {
		t.Errorf("refs = %q, want %q", refs, want)
	}
}

func TestNextLink(t *testing.T) {
	h := `<https://api.github.com/user/repos?page=2>; rel="next", <https://api.github.com/user/repos?page=5>; rel="last"`
	if got := nextLink(h); got != "https://api.github.com/user/repos?page=2" {
		t.Errorf("nextLink = %q", got)
	}
	if got := nextLink(`<https://x/?page=1>; rel="prev"`); got != "" {
		t.Errorf("nextLink = %q, want empty", got)
	}
}

func TestRateLimitDelay(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	mk := func(status int, hdr map[string]string) *http.Response {
		r := &http.Response{StatusCode: status, Header: http.Header{}}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		resp *http.Response
		msg  string
		want time.Duration
		ok   bool
	}{
		{mk(403, map[string]string{"Retry-After": "10"}), "", 10 * time.Second, true},
		{mk(403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1000100"}), "", 101 * time.Second, true},
		{mk(403, nil), "You have exceeded a secondary rate limit", time.Minute, true},
		{mk(429, nil), "", time.Minute, true},
		{mk(403, nil), "Must have admin rights to Repository.", 0, false},
		{mk(404, map[string]string{"Retry-After": "10"}), "", 0, false},
	}
	for i, c := range cases {
		got, ok := rateLimitDelay(c.resp, c.msg, now)
		if got != c.want || ok != c.ok {
			t.Errorf("case %d: got %v, %v; want %v, %v", i, got, ok, c.want, c.ok)
		}
	}
}

func TestResolveToken(t *testing.T) {
	origEnv, origGH := getenv, ghAuthToken
	defer func() { getenv, ghAuthToken = origEnv, origGH }()
	env := map[string]string{}
	getenv = func(k string) string { return env[k] }
	var ghHost string
	ghAuthToken = func(_ context.Context, host string) (string, error) { ghHost = host; return "from-gh", nil }

	ctx := context.Background()
	if tok, src, _ := resolveToken(ctx, "", defaultAPIURL); tok != "from-gh" || src != "gh auth token" || ghHost != "github.com" {
		t.Errorf("gh fallback: %q %q host=%q", tok, src, ghHost)
	}
	if _, _, _ = resolveToken(ctx, "", "https://ghe.example.com/api/v3"); ghHost != "ghe.example.com" {
		t.Errorf("GHES host = %q", ghHost)
	}
	env["GITHUB_TOKEN"] = "gt"
	if tok, _, _ := resolveToken(ctx, "", defaultAPIURL); tok != "gt" {
		t.Errorf("GITHUB_TOKEN: %q", tok)
	}
	env["GH_TOKEN"] = "ght"
	if tok, _, _ := resolveToken(ctx, "", defaultAPIURL); tok != "ght" {
		t.Errorf("GH_TOKEN should win: %q", tok)
	}
	if tok, _, _ := resolveToken(ctx, "flag", defaultAPIURL); tok != "flag" {
		t.Errorf("--token should win: %q", tok)
	}
	env = map[string]string{}
	ghAuthToken = func(context.Context, string) (string, error) { return "", errors.New("missing") }
	if _, _, err := resolveToken(ctx, "", defaultAPIURL); err == nil {
		t.Error("expected error without any token")
	}
}

func TestAPIErrorNonJSONBody(t *testing.T) {
	body := "<html>\n  <head><title>Bad Gateway</title></head>\n  <body>" + strings.Repeat("x", 300) + "</body>\n</html>"
	got := newAPIError("GET", "/user", 502, []byte(body)).Error()
	if strings.Contains(got, "\n") || !strings.HasPrefix(got, "GET /user: HTTP 502: <html> <head><title>Bad Gateway</title></head>") || !strings.HasSuffix(got, "...") {
		t.Errorf("got %q", got)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	body := `{"message":"Validation Failed","errors":[{"resource":"Repository","code":"custom","message":"name already exists"},{"resource":"Repository","field":"name","code":"invalid"},"plain"]}`
	e := newAPIError("POST", "/repos/a/b/transfer", 422, []byte(body))
	want := "POST /repos/a/b/transfer: HTTP 422: Validation Failed (name already exists; Repository name invalid; plain)"
	if e.Error() != want {
		t.Errorf("got  %s\nwant %s", e.Error(), want)
	}
}

// ---- interactive mode -------------------------------------------------------

func TestInteractiveTransfer(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	// Repositories are offered least recently pushed first:
	// 1 empty, 2 old-fork, 3 old-thing, 4 bar, 5 baz, 6 foo, 7 keep-me
	input := strings.Join([]string{
		"1",           // FROM: alice (you)
		"1",           // TO: archive-org
		"3", "5", "6", // tick old-thing, baz (name taken in archive-org), foo
		"0", // done selecting
		"y", // archive after moving
		"y", // confirm
	}, "\n") + "\n"
	res := runCLI(t, srv, input, "interactive", "--accessible", "--interval", "0")
	if res.code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	if want := []string{"alice/old-thing->archive-org", "alice/foo->archive-org"}; fmt.Sprint(f.transfers) != fmt.Sprint(want) {
		t.Errorf("transfers = %v, want %v", f.transfers, want)
	}
	if want := []string{"archive-org/old-thing", "archive-org/foo"}; fmt.Sprint(f.archived) != fmt.Sprint(want) {
		t.Errorf("archived = %v, want %v", f.archived, want)
	}
	for _, want := range []string{"From: alice  ->  To: archive-org", "archive-org/baz already exists", "Done: 2 transferred"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout missing %q\n%s", want, res.stdout)
		}
	}
}

func TestInteractiveOtherDestinationAndCancel(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	input := strings.Join([]string{
		"1",      // FROM: alice
		"3",      // TO: Other...
		"nobody", // does not exist -> the name is asked again
		"bob",    // a user
		"6", "0", // tick foo, done
		"n", // do not archive
		"n", // cancel at the final confirmation
	}, "\n") + "\n"
	res := runCLI(t, srv, input, "interactive", "--accessible")
	if res.code != 1 || len(f.transfers) != 0 {
		t.Fatalf("code=%d transfers=%v\n%s\n%s", res.code, f.transfers, res.stdout, res.stderr)
	}
	for _, want := range []string{`"nobody" does not exist`, "From: alice  ->  To: bob", "1 to transfer", "Cancelled"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout missing %q\n%s", want, res.stdout)
		}
	}
}

func TestInteractiveNothingSelected(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	// FROM archive-org (2), TO other-org (1: archive-org itself is not offered), select nothing
	res := runCLI(t, srv, "2\n1\n0\nn\n", "interactive", "--accessible")
	if res.code != 0 || len(f.transfers) != 0 || !strings.Contains(res.stdout, "No repositories selected") {
		t.Fatalf("code=%d transfers=%v\n%s\n%s", res.code, f.transfers, res.stdout, res.stderr)
	}
}

func TestInteractiveNeedsTerminal(t *testing.T) {
	if res := runCLI(t, nil, "", "interactive"); res.code != 2 || !strings.Contains(res.stderr, "needs a terminal") {
		t.Errorf("code=%d stderr=%s", res.code, res.stderr)
	}
	if res := runCLI(t, nil, ""); res.code != 2 || !strings.Contains(res.stderr, "Usage:") {
		t.Errorf("no args without a terminal should print usage: code=%d stderr=%s", res.code, res.stderr)
	}
}

func TestRepoLabel(t *testing.T) {
	pushed := time.Date(2021, 3, 4, 0, 0, 0, 0, time.UTC)
	r := &Repo{Name: "a-very-long-repository-name", Visibility: "private", Fork: true, Stars: 3, PushedAt: &pushed, Description: "  hello\n world "}
	got := repoLabel(r, 10)
	want := "a-very-lo~  2021-03-04    [private, fork, stars:3]  hello world"
	if got != want {
		t.Errorf("repoLabel = %q\nwant        %q", got, want)
	}
	if got := repoLabel(&Repo{Name: "x"}, 3); got != "x    never pushed" {
		t.Errorf("repoLabel = %q", got)
	}
}

func TestInteractiveInputEndsEarly(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	// An invalid answer followed by end of input must not crash.
	res := runCLI(t, srv, "1\nxyz\n", "interactive", "--accessible")
	if res.code != 1 || len(f.transfers) != 0 || !strings.Contains(res.stderr, "input ended") {
		t.Fatalf("code=%d transfers=%v\n%s\n%s", res.code, f.transfers, res.stdout, res.stderr)
	}
}

func TestInteractiveFromFlagAndOwnerLabels(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	f.ownerOf = map[string]bool{"other-org": true} // owner orgs are listed first
	// --from skips the FROM question; TO offers: 1 other-org (owner), 2 Other...
	res := runCLI(t, srv, "1\n0\nn\n", "interactive", "--accessible", "--from", "archive-org")
	if res.code != 0 {
		t.Fatalf("code=%d\n%s\n%s", res.code, res.stdout, res.stderr)
	}
	for _, want := range []string{"1. other-org (owner)", "From: archive-org  ->  To: other-org", "No repositories selected"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout missing %q\n%s", want, res.stdout)
		}
	}
	if strings.Contains(res.stdout, "Move repositories FROM") {
		t.Errorf("FROM should not be asked with --from:\n%s", res.stdout)
	}
}

func TestMyOrgs(t *testing.T) {
	f, srv := standardFake()
	defer srv.Close()
	f.orgs = []string{"b-member", "z-owner", "a-member"}
	f.ownerOf = map[string]bool{"z-owner": true}
	c := NewClient(srv.URL, "test-token", io.Discard)
	got, err := c.MyOrgs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := "[{z-owner admin} {a-member member} {b-member member}]"; fmt.Sprint(got) != want {
		t.Errorf("MyOrgs = %v, want %s", got, want)
	}
	if l := got[0].label(); l != "z-owner (owner)" {
		t.Errorf("label = %q", l)
	}

	f.noMembershipsAPI = true // fall back to /user/orgs without roles
	got, err = c.MyOrgs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := "[{a-member } {b-member } {z-owner }]"; fmt.Sprint(got) != want {
		t.Errorf("fallback MyOrgs = %v, want %s", got, want)
	}
	if l := got[0].label(); l != "a-member" {
		t.Errorf("label = %q", l)
	}
}

func TestDetectProgName(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/ghbt": "ghbt",
		"ghbt.exe":            "ghbt",
		"/home/u/.local/share/gh/extensions/gh-bulk-repo-transfer/gh-bulk-repo-transfer": "gh bulk-repo-transfer",
		"gh-bulk-repo-transfer.exe":  "gh bulk-repo-transfer",
		"/tmp/go-build/ghbt.test":    "ghbt",
		"/opt/something/else-binary": "ghbt",
	}
	for in, want := range cases {
		if got := detectProgName(in); got != want {
			t.Errorf("detectProgName(%q) = %q, want %q", in, got, want)
		}
	}
	orig := progName
	defer func() { progName = orig }()
	progName = "gh bulk-repo-transfer"
	if got := named(`Run "ghbt transfer -h"`); got != `Run "gh bulk-repo-transfer transfer -h"` {
		t.Errorf("named = %q", got)
	}
}
