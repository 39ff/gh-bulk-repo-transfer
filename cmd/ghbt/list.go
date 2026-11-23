package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// filterFlags are the repository selection flags shared by list and transfer.
type filterFlags struct {
	match, exclude, visibility, archived, forks, pushedBefore string
}

func (f *filterFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.match, "match", "", "only repositories whose name matches this regexp (case-insensitive)")
	fs.StringVar(&f.exclude, "exclude", "", "skip repositories whose name matches this regexp (case-insensitive)")
	fs.StringVar(&f.visibility, "visibility", "all", "all, public, private or internal")
	fs.StringVar(&f.archived, "archived", "any", "archived repositories: any, only or exclude")
	fs.StringVar(&f.forks, "forks", "any", "forked repositories: any, only or exclude")
	fs.StringVar(&f.pushedBefore, "pushed-before", "", "only repositories last pushed before this date (YYYY-MM-DD); never-pushed ones match too")
}

type repoFilter struct {
	match, exclude              *regexp.Regexp
	visibility, archived, forks string
	pushedBefore                *time.Time
	active                      bool
}

func (f *filterFlags) compile() (*repoFilter, error) {
	rf := &repoFilter{
		visibility: strings.ToLower(f.visibility),
		archived:   strings.ToLower(f.archived),
		forks:      strings.ToLower(f.forks),
	}
	var err error
	if f.match != "" {
		if rf.match, err = regexp.Compile("(?i)" + f.match); err != nil {
			return nil, fmt.Errorf("--match: %w", err)
		}
	}
	if f.exclude != "" {
		if rf.exclude, err = regexp.Compile("(?i)" + f.exclude); err != nil {
			return nil, fmt.Errorf("--exclude: %w", err)
		}
	}
	switch rf.visibility {
	case "all", "public", "private", "internal":
	default:
		return nil, fmt.Errorf("--visibility: must be all, public, private or internal (got %q)", f.visibility)
	}
	for flagName, v := range map[string]string{"--archived": rf.archived, "--forks": rf.forks} {
		switch v {
		case "any", "only", "exclude":
		default:
			return nil, fmt.Errorf("%s: must be any, only or exclude (got %q)", flagName, v)
		}
	}
	if f.pushedBefore != "" {
		t, err := time.Parse("2006-01-02", f.pushedBefore)
		if err != nil {
			return nil, fmt.Errorf("--pushed-before: expected YYYY-MM-DD (got %q)", f.pushedBefore)
		}
		rf.pushedBefore = &t
	}
	rf.active = rf.match != nil || rf.exclude != nil || rf.visibility != "all" ||
		rf.archived != "any" || rf.forks != "any" || rf.pushedBefore != nil
	return rf, nil
}

func (f *repoFilter) keep(r *Repo) bool {
	if f.match != nil && !f.match.MatchString(r.Name) {
		return false
	}
	if f.exclude != nil && f.exclude.MatchString(r.Name) {
		return false
	}
	if f.visibility != "all" && r.visibility() != f.visibility {
		return false
	}
	if !triState(f.archived, r.Archived) || !triState(f.forks, r.Fork) {
		return false
	}
	if f.pushedBefore != nil && r.PushedAt != nil && !r.PushedAt.Before(*f.pushedBefore) {
		return false
	}
	return true
}

func triState(mode string, v bool) bool {
	switch mode {
	case "only":
		return v
	case "exclude":
		return !v
	}
	return true
}

func sortRepos(repos []*Repo, by string) {
	sort.SliceStable(repos, func(i, j int) bool {
		a, b := repos[i], repos[j]
		if by == "pushed" {
			switch {
			case a.PushedAt == nil && b.PushedAt != nil:
				return true
			case a.PushedAt != nil && b.PushedAt == nil:
				return false
			case a.PushedAt != nil && b.PushedAt != nil && !a.PushedAt.Equal(*b.PushedAt):
				return a.PushedAt.Before(*b.PushedAt)
			}
		}
		return strings.ToLower(a.FullName) < strings.ToLower(b.FullName)
	})
}

const listSynopsis = `Usage: ghbt list [flags]

List repositories of --from (default: you). The text output is a list you can
edit and pass to "ghbt transfer --file".

Examples:
  ghbt list --sort pushed -o repos.txt
  ghbt list --pushed-before 2023-01-01 --forks exclude
  ghbt list --from my-org --format json
`

func cmdList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("list", listSynopsis, stderr)
	var cf commonFlags
	cf.register(fs)
	var ff filterFlags
	ff.register(fs)
	var output, format, sortBy string
	fs.StringVar(&output, "o", "", "write the list to this file instead of stdout")
	fs.StringVar(&output, "output", "", "same as -o")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	fs.StringVar(&sortBy, "sort", "name", "sort order: name, or pushed (least recently pushed first)")

	positional, err := parseArgs(fs, args)
	if err != nil {
		return flagErrCode(err)
	}
	if len(positional) > 0 {
		fmt.Fprintf(stderr, "error: unexpected argument %q\n", positional[0])
		return 2
	}
	if format != "text" && format != "json" {
		fmt.Fprintf(stderr, "error: --format must be text or json (got %q)\n", format)
		return 2
	}
	if sortBy != "name" && sortBy != "pushed" {
		fmt.Fprintf(stderr, "error: --sort must be name or pushed (got %q)\n", sortBy)
		return 2
	}
	filter, err := ff.compile()
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	client, me, err := connect(ctx, cf, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	owner := cf.from
	if owner == "" {
		owner = me.Login
	}
	all, err := client.ListRepos(ctx, owner, me)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	var repos []*Repo
	for i := range all {
		if filter.keep(&all[i]) {
			repos = append(repos, &all[i])
		}
	}
	sortRepos(repos, sortBy)

	w := stdout
	var f *os.File
	if output != "" && output != "-" {
		if f, err = os.Create(output); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		w = f
	}
	if format == "json" {
		err = writeRepoJSON(w, repos)
	} else {
		err = writeRepoText(w, repos, owner, len(all))
	}
	if f != nil {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if f != nil {
		fmt.Fprintf(stderr, "Wrote %d of %d repositories to %s\n", len(repos), len(all), output)
	}
	return 0
}

func writeRepoText(w io.Writer, repos []*Repo, owner string, total int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %d of %d repositories owned by %s (ghbt list, %s)\n", len(repos), total, owner, nowFn().Format("2006-01-02"))
	b.WriteString("# Keep only the lines of repositories you want to TRANSFER.\n")
	b.WriteString("# Delete a line (or put '#' in front of it) to leave that repository where it is.\n")
	b.WriteString("# Then run: ghbt transfer --to <ORG> --file <this file> --dry-run\n")
	width := 0
	for _, r := range repos {
		width = max(width, len(r.FullName))
	}
	for _, r := range repos {
		fmt.Fprintf(&b, "%-*s  # %s\n", width, r.FullName, describeRepo(r))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func describeRepo(r *Repo) string {
	parts := []string{r.visibility()}
	if r.Fork {
		parts = append(parts, "fork")
	}
	if r.Archived {
		parts = append(parts, "archived")
	}
	if r.PushedAt != nil {
		parts = append(parts, "pushed:"+r.PushedAt.Format("2006-01-02"))
	} else {
		parts = append(parts, "pushed:never")
	}
	parts = append(parts, fmt.Sprintf("stars:%d", r.Stars))
	if d := strings.Join(strings.Fields(r.Description), " "); d != "" {
		if rs := []rune(d); len(rs) > 60 {
			d = string(rs[:60]) + "..."
		}
		parts = append(parts, d)
	}
	return strings.Join(parts, "  ")
}

type repoJSON struct {
	FullName    string     `json:"full_name"`
	Owner       string     `json:"owner"`
	Name        string     `json:"name"`
	Visibility  string     `json:"visibility"`
	Fork        bool       `json:"fork"`
	Archived    bool       `json:"archived"`
	PushedAt    *time.Time `json:"pushed_at"`
	Stars       int        `json:"stars"`
	Description string     `json:"description"`
	URL         string     `json:"url"`
}

func writeRepoJSON(w io.Writer, repos []*Repo) error {
	items := make([]repoJSON, 0, len(repos))
	for _, r := range repos {
		items = append(items, repoJSON{
			FullName: r.FullName, Owner: r.Owner.Login, Name: r.Name,
			Visibility: r.visibility(), Fork: r.Fork, Archived: r.Archived,
			PushedAt: r.PushedAt, Stars: r.Stars, Description: r.Description, URL: r.HTMLURL,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}
