package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIURL    = "https://api.github.com"
	maxRateLimitWait = time.Hour
)

// Account is a GitHub user or organization.
type Account struct {
	Login string `json:"login"`
	Type  string `json:"type"` // "User" or "Organization"
}

// Permissions are the authenticated user's permissions on a repository.
type Permissions struct {
	Admin bool `json:"admin"`
}

// Repo is the subset of the GitHub repository object this tool uses.
type Repo struct {
	Name        string       `json:"name"`
	FullName    string       `json:"full_name"`
	Owner       Account      `json:"owner"`
	Private     bool         `json:"private"`
	Visibility  string       `json:"visibility,omitempty"`
	Fork        bool         `json:"fork"`
	Archived    bool         `json:"archived"`
	Description string       `json:"description"`
	PushedAt    *time.Time   `json:"pushed_at"`
	Stars       int          `json:"stargazers_count"`
	HTMLURL     string       `json:"html_url"`
	Permissions *Permissions `json:"permissions,omitempty"`
}

func (r *Repo) visibility() string {
	if r.Visibility != "" {
		return strings.ToLower(r.Visibility)
	}
	if r.Private {
		return "private"
	}
	return "public"
}

// APIError is a non-2xx response from the GitHub API.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Message    string
	Details    []string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if len(e.Details) > 0 {
		msg += " (" + strings.Join(e.Details, "; ") + ")"
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, msg)
}

func newAPIError(method, path string, status int, body []byte) *APIError {
	e := &APIError{StatusCode: status, Method: method, Path: path}
	var payload struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		// Not JSON (e.g. an HTML page from a proxy): show a short single line.
		msg := strings.Join(strings.Fields(string(body)), " ")
		if r := []rune(msg); len(r) > 120 {
			msg = string(r[:120]) + "..."
		}
		e.Message = msg
		return e
	}
	e.Message = payload.Message
	for _, raw := range payload.Errors {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			e.Details = append(e.Details, s)
			continue
		}
		var obj struct {
			Resource string `json:"resource"`
			Field    string `json:"field"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		}
		if json.Unmarshal(raw, &obj) != nil {
			continue
		}
		if obj.Message != "" {
			e.Details = append(e.Details, obj.Message)
		} else if obj.Code != "" {
			e.Details = append(e.Details, strings.Join(strings.Fields(obj.Resource+" "+obj.Field+" "+obj.Code), " "))
		}
	}
	return e
}

func isNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound
}

// Client is a minimal GitHub REST API client with retry and rate-limit handling.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	log        io.Writer
	maxRetries int
}

func NewClient(baseURL, token string, log io.Writer) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		log:        log,
		maxRetries: 4,
	}
}

// do sends a request and decodes a JSON response into out (if non-nil).
// path is either relative to the base URL or an absolute URL.
func (c *Client) do(ctx context.Context, method, path string, body, out any) (*http.Response, error) {
	u := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		u = c.baseURL + path
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}

	for attempt := 0; ; attempt++ {
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "ghbt/"+version)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		var data []byte
		if err == nil {
			data, err = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Only GETs are retried on network errors: a POST may have been applied.
			if method == http.MethodGet && attempt < c.maxRetries {
				if werr := c.wait(ctx, backoff(attempt), fmt.Sprintf("request failed (%v)", err)); werr != nil {
					return nil, werr
				}
				continue
			}
			return nil, err
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out != nil && len(bytes.TrimSpace(data)) > 0 {
				if err := json.Unmarshal(data, out); err != nil {
					return resp, fmt.Errorf("%s %s: decoding response: %w", method, path, err)
				}
			}
			return resp, nil
		}

		apiErr := newAPIError(method, path, resp.StatusCode, data)
		if attempt < c.maxRetries {
			if d, ok := rateLimitDelay(resp, apiErr.Message, nowFn()); ok {
				if werr := c.wait(ctx, d, "rate limited by GitHub"); werr != nil {
					return nil, werr
				}
				continue
			}
			if resp.StatusCode >= 500 && method == http.MethodGet {
				if werr := c.wait(ctx, backoff(attempt), fmt.Sprintf("server error (HTTP %d)", resp.StatusCode)); werr != nil {
					return nil, werr
				}
				continue
			}
		}
		return resp, apiErr
	}
}

func (c *Client) wait(ctx context.Context, d time.Duration, reason string) error {
	fmt.Fprintf(c.log, "%s; retrying in %s...\n", reason, d.Round(time.Second))
	return sleepFn(ctx, d)
}

func backoff(attempt int) time.Duration {
	return time.Duration(1<<attempt) * time.Second
}

// rateLimitDelay reports whether resp is a (primary or secondary) rate-limit
// response and how long to wait before retrying.
func rateLimitDelay(resp *http.Response, message string, now time.Time) (time.Duration, bool) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}
	clamp := func(d time.Duration) time.Duration {
		return min(max(d, time.Second), maxRateLimitWait)
	}
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return clamp(time.Duration(secs) * time.Second), true
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return clamp(time.Unix(reset, 0).Sub(now) + time.Second), true
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests || strings.Contains(strings.ToLower(message), "rate limit") {
		return time.Minute, true
	}
	return 0, false
}

// nextLink extracts the rel="next" URL from a Link header.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		target := strings.TrimSpace(segs[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, p := range segs[1:] {
			if strings.TrimSpace(p) == `rel="next"` {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}

func (c *Client) sameOrigin(raw string) bool {
	a, err1 := url.Parse(raw)
	b, err2 := url.Parse(c.baseURL)
	return err1 == nil && err2 == nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// getAll fetches every page of a list endpoint.
func getAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var all []T
	for next := path; next != ""; {
		var page []T
		resp, err := c.do(ctx, http.MethodGet, next, nil, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		next = nextLink(resp.Header.Get("Link"))
		if next != "" && !c.sameOrigin(next) {
			return nil, fmt.Errorf("refusing to follow pagination link to another host: %s", next)
		}
	}
	return all, nil
}

func (c *Client) CurrentUser(ctx context.Context) (*Account, error) {
	var a Account
	if _, err := c.do(ctx, http.MethodGet, "/user", nil, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (c *Client) GetAccount(ctx context.Context, login string) (*Account, error) {
	var a Account
	if _, err := c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(login), nil, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// GetRepo fetches a repository. Redirects (renamed or transferred
// repositories) are followed, so the result may have a different full name.
func (c *Client) GetRepo(ctx context.Context, owner, name string) (*Repo, error) {
	var r Repo
	if _, err := c.do(ctx, http.MethodGet, repoPath(owner, name), nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRepos lists the repositories owned by owner.
func (c *Client) ListRepos(ctx context.Context, owner string, me *Account) ([]Repo, error) {
	if strings.EqualFold(owner, me.Login) {
		// Includes private repositories of the authenticated user.
		return getAll[Repo](ctx, c, "/user/repos?affiliation=owner&per_page=100")
	}
	acct, err := c.GetAccount(ctx, owner)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("owner %q not found", owner)
		}
		return nil, err
	}
	if acct.Type == "Organization" {
		return getAll[Repo](ctx, c, "/orgs/"+url.PathEscape(acct.Login)+"/repos?type=all&per_page=100")
	}
	return getAll[Repo](ctx, c, "/users/"+url.PathEscape(acct.Login)+"/repos?type=owner&per_page=100")
}

// OrgMembership is an organization the authenticated user belongs to.
type OrgMembership struct {
	Login string
	Role  string // "admin" (= owner), "member", or "" if unknown
}

// MyOrgs lists the organizations the authenticated user belongs to, owners
// first. Classic tokens need the read:org scope to see the role and all orgs.
func (c *Client) MyOrgs(ctx context.Context) ([]OrgMembership, error) {
	var out []OrgMembership
	type membership struct {
		Role         string  `json:"role"`
		Organization Account `json:"organization"`
	}
	ms, err := getAll[membership](ctx, c, "/user/memberships/orgs?state=active&per_page=100")
	if err == nil {
		for _, m := range ms {
			out = append(out, OrgMembership{Login: m.Organization.Login, Role: m.Role})
		}
	} else {
		// Fallback without roles (e.g. tokens without read:org).
		orgs, err2 := getAll[Account](ctx, c, "/user/orgs?per_page=100")
		if err2 != nil {
			return nil, err
		}
		for _, o := range orgs {
			out = append(out, OrgMembership{Login: o.Login})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Role == "admin") != (out[j].Role == "admin") {
			return out[i].Role == "admin"
		}
		return strings.ToLower(out[i].Login) < strings.ToLower(out[j].Login)
	})
	return out, nil
}

// TransferRepo starts the transfer of owner/name to newOwner.
// GitHub answers 202 Accepted; the move itself happens asynchronously.
func (c *Client) TransferRepo(ctx context.Context, owner, name, newOwner string) error {
	body := map[string]any{"new_owner": newOwner}
	_, err := c.do(ctx, http.MethodPost, repoPath(owner, name)+"/transfer", body, nil)
	return err
}

func (c *Client) ArchiveRepo(ctx context.Context, owner, name string) error {
	_, err := c.do(ctx, http.MethodPatch, repoPath(owner, name), map[string]any{"archived": true}, nil)
	return err
}

func repoPath(owner, name string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// ghAuthToken asks the GitHub CLI for its token. Replaced in tests.
var ghAuthToken = func(ctx context.Context, host string) (string, error) {
	bin, err := exec.LookPath("gh")
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, bin, "auth", "token", "--hostname", host).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func resolveToken(ctx context.Context, flagToken, apiURL string) (token, source string, err error) {
	if t := strings.TrimSpace(flagToken); t != "" {
		return t, "--token", nil
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v, "$" + name, nil
		}
	}
	host := "github.com"
	if u, err := url.Parse(apiURL); err == nil && u.Hostname() != "" && !strings.EqualFold(u.Hostname(), "api.github.com") {
		host = u.Hostname()
	}
	if t, err := ghAuthToken(ctx, host); err == nil && t != "" {
		return t, "gh auth token", nil
	}
	return "", "", errors.New(`no GitHub token found: set GH_TOKEN (or GITHUB_TOKEN), pass --token, or log in with "gh auth login"`)
}
