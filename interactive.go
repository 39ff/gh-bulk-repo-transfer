package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/term"
)

const interactiveSynopsis = `Usage: ghbt interactive [flags]      (running "ghbt" with no arguments does the same)

Step by step in the terminal:
  1. choose where to move repositories FROM and TO
  2. tick the repositories to move (space: select, /: filter, ctrl+a: all)
  3. review the plan and confirm
`

// otherOwner is the option value for "type a name yourself".
const otherOwner = "\x00other"

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

func cmdInteractive(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("interactive", interactiveSynopsis, stderr)
	var cf commonFlags
	cf.register(fs)
	var accessible bool
	var interval time.Duration
	fs.BoolVar(&accessible, "accessible", getenv("ACCESSIBLE") != "", "use simple numbered prompts instead of the full-screen UI (also: $ACCESSIBLE)")
	fs.DurationVar(&interval, "interval", 2*time.Second, "pause between transfers (avoids GitHub rate limits)")
	positional, err := parseArgs(fs, args)
	if err != nil {
		return flagErrCode(err)
	}
	if len(positional) > 0 {
		fmt.Fprintf(stderr, "error: unexpected argument %q\n", positional[0])
		return 2
	}
	if !accessible && !isTerminal(stdin) {
		fmt.Fprintln(stderr, `error: interactive mode needs a terminal; use "ghbt list" and "ghbt transfer" in scripts`)
		return 2
	}

	w := &wizard{ctx: ctx, in: stdin, out: stdout, errOut: stderr, accessible: accessible}
	code, err := w.run(cf, interval)
	switch {
	case errors.Is(err, huh.ErrUserAborted):
		fmt.Fprintln(stdout, "Aborted. Nothing was transferred.")
		return 130
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return code
}

type wizard struct {
	ctx         context.Context
	in          io.Reader
	out, errOut io.Writer
	accessible  bool
}

func (w *wizard) ask(fields ...huh.Field) (err error) {
	// huh's plain-text prompts can panic when input ends after an invalid
	// answer (e.g. Ctrl+D); report that as an error instead of crashing.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("input ended unexpectedly (%v)", r)
		}
	}()
	return huh.NewForm(huh.NewGroup(fields...)).
		WithAccessible(w.accessible).
		WithInput(w.in).
		WithOutput(w.out).
		RunWithContext(w.ctx)
}

func (w *wizard) run(cf commonFlags, interval time.Duration) (int, error) {
	client, me, err := w.connect(cf)
	if err != nil {
		return 1, err
	}
	orgs, err := client.MyOrgs(w.ctx)
	if err != nil {
		fmt.Fprintf(w.errOut, "Could not list your organizations (%v); you can type names instead.\n", err)
		orgs = nil
	}

	// 1. source and destination
	source := cf.from
	if source == "" {
		if source, err = w.chooseSource(me, orgs); err != nil {
			return 1, err
		}
	}
	dest, err := w.chooseDestination(client, orgs, source)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(w.out, "From: %s  ->  To: %s\n", source, dest.Login)

	// 2. repositories
	fmt.Fprintf(w.errOut, "Loading repositories of %s...\n", source)
	all, err := client.ListRepos(w.ctx, source, me)
	if err != nil {
		return 1, err
	}
	var candidates []*Repo
	noAdmin := 0
	for i := range all {
		r := &all[i]
		if r.Permissions != nil && !r.Permissions.Admin {
			noAdmin++
			continue
		}
		candidates = append(candidates, r)
	}
	if noAdmin > 0 {
		fmt.Fprintf(w.out, "(%d repositories are hidden because you are not an admin of them)\n", noAdmin)
	}
	if len(candidates) == 0 {
		fmt.Fprintf(w.out, "%s has no repositories you can transfer.\n", source)
		return 0, nil
	}
	sortRepos(candidates, "pushed")

	selected, archive, err := w.chooseRepos(candidates, dest.Login)
	if err != nil {
		return 1, err
	}
	if len(selected) == 0 {
		fmt.Fprintln(w.out, "No repositories selected. Nothing was transferred.")
		return 0, nil
	}

	// 3. review and confirm
	fmt.Fprintf(w.errOut, "Checking %d repositories...\n", len(selected))
	entries, err := planRepos(w.ctx, client, selected, dest)
	if err != nil {
		return 1, err
	}
	nTransfer, _, nErr := printPlan(w.out, entries, dest)
	if nTransfer == 0 {
		fmt.Fprintln(w.out, "Nothing to transfer.")
		if nErr > 0 {
			return 1, nil
		}
		return 0, nil
	}
	what := "Transfer"
	if archive {
		what = "Transfer and archive"
	}
	ok := false
	err = w.ask(huh.NewConfirm().
		Title(fmt.Sprintf("%s %d repositories to %s?", what, nTransfer, dest.Login)).
		Description("Issues, pull requests, wiki and stars move with them; old URLs redirect.").
		Affirmative("Transfer").
		Negative("Cancel").
		Value(&ok))
	if err != nil {
		return 1, err
	}
	if !ok {
		fmt.Fprintln(w.out, "Cancelled. Nothing was transferred.")
		return 1, nil
	}
	fmt.Fprintln(w.out)
	return executePlan(w.ctx, client, me, dest, entries, archive, interval, w.out, w.errOut), nil
}

// connect uses the usual token sources and asks for a token if none is found.
func (w *wizard) connect(cf commonFlags) (*Client, *Account, error) {
	apiURL := cf.resolvedAPIURL()
	token, source, err := resolveToken(w.ctx, cf.token, apiURL)
	if err != nil {
		var input string
		err := w.ask(
			huh.NewNote().
				Title("GitHub token needed").
				Description("No token found ($GH_TOKEN, $GITHUB_TOKEN or \"gh auth login\").\n"+
					"Create a classic token with the \"repo\" scope at https://github.com/settings/tokens\n"+
					"It is used for this run only and is not saved."),
			huh.NewInput().
				Title("Paste your token").
				EchoMode(huh.EchoModePassword).
				Value(&input),
		)
		if err != nil {
			return nil, nil, err
		}
		if token = strings.TrimSpace(input); token == "" {
			return nil, nil, errors.New("no token given")
		}
		source = "prompt"
	}
	return login(w.ctx, apiURL, token, source, w.errOut)
}

func (w *wizard) chooseSource(me *Account, orgs []Account) (string, error) {
	opts := []huh.Option[string]{huh.NewOption(me.Login+" (you)", me.Login)}
	for _, o := range orgs {
		opts = append(opts, huh.NewOption(o.Login, o.Login))
	}
	opts = append(opts, huh.NewOption("Other (type a name)...", otherOwner))
	value := me.Login
	if err := w.ask(huh.NewSelect[string]().Title("Move repositories FROM").Options(opts...).Value(&value)); err != nil {
		return "", err
	}
	if value == otherOwner {
		return w.askName("Source owner (user or organization)")
	}
	return value, nil
}

func (w *wizard) chooseDestination(c *Client, orgs []Account, source string) (*Account, error) {
	for {
		var opts []huh.Option[string]
		for _, o := range orgs {
			if !strings.EqualFold(o.Login, source) {
				opts = append(opts, huh.NewOption(o.Login, o.Login))
			}
		}
		var name string
		if len(opts) == 0 {
			var err error
			if name, err = w.askName("Move repositories TO (organization name)"); err != nil {
				return nil, err
			}
		} else {
			opts = append(opts, huh.NewOption("Other (type a name)...", otherOwner))
			name = opts[0].Value
			if err := w.ask(huh.NewSelect[string]().Title("Move repositories TO").Options(opts...).Value(&name)); err != nil {
				return nil, err
			}
			if name == otherOwner {
				var err error
				if name, err = w.askName("Destination (organization name)"); err != nil {
					return nil, err
				}
			}
		}

		acct, err := c.GetAccount(w.ctx, name)
		switch {
		case isNotFound(err):
			fmt.Fprintf(w.out, "%q does not exist. Try again.\n", name)
		case err != nil:
			return nil, err
		case strings.EqualFold(acct.Login, source):
			fmt.Fprintln(w.out, "The destination must be different from the source. Try again.")
		default:
			return acct, nil
		}
	}
}

func (w *wizard) askName(title string) (string, error) {
	var name string
	err := w.ask(huh.NewInput().Title(title).Value(&name).Validate(func(s string) error {
		if !validName(strings.TrimSpace(s)) {
			return errors.New("enter a valid user or organization name")
		}
		return nil
	}))
	if err != nil {
		return "", err
	}
	if name = strings.TrimSpace(name); !validName(name) {
		return "", errors.New("no valid name given")
	}
	return name, nil
}

func (w *wizard) chooseRepos(repos []*Repo, dest string) ([]*Repo, bool, error) {
	width := 0
	for _, r := range repos {
		width = max(width, len(r.Name))
	}
	width = min(width, 40)
	byName := make(map[string]*Repo, len(repos))
	opts := make([]huh.Option[string], 0, len(repos))
	for _, r := range repos {
		byName[r.FullName] = r
		opts = append(opts, huh.NewOption(repoLabel(r, width), r.FullName))
	}

	var picked []string
	archive := false
	err := w.ask(
		huh.NewMultiSelect[string]().
			Title(fmt.Sprintf("Select repositories to move to %s (%d, least recently pushed first)", dest, len(repos))).
			Description("space/x: select   /: filter   ctrl+a: select all   enter: done").
			Options(opts...).
			Height(min(len(opts), 15)+4).
			Value(&picked),
		huh.NewConfirm().
			Title("Archive them after moving?").
			Description("Archived repositories become read-only. This can be undone later.").
			Value(&archive),
	)
	if err != nil {
		return nil, false, err
	}
	selected := make([]*Repo, 0, len(picked))
	for _, r := range repos { // keep the list order
		for _, p := range picked {
			if p == r.FullName {
				selected = append(selected, byName[p])
				break
			}
		}
	}
	return selected, archive, nil
}

func repoLabel(r *Repo, width int) string {
	name := r.Name
	if len(name) > width {
		name = name[:width-1] + "~"
	}
	pushed := "never pushed"
	if r.PushedAt != nil {
		pushed = r.PushedAt.Format("2006-01-02")
	}
	s := fmt.Sprintf("%-*s  %-12s", width, name, pushed)
	var tags []string
	if v := r.visibility(); v != "public" {
		tags = append(tags, v)
	}
	if r.Fork {
		tags = append(tags, "fork")
	}
	if r.Archived {
		tags = append(tags, "archived")
	}
	if r.Stars > 0 {
		tags = append(tags, fmt.Sprintf("stars:%d", r.Stars))
	}
	if len(tags) > 0 {
		s += "  [" + strings.Join(tags, ", ") + "]"
	}
	if d := strings.Join(strings.Fields(r.Description), " "); d != "" {
		if rs := []rune(d); len(rs) > 40 {
			d = string(rs[:40]) + "..."
		}
		s += "  " + d
	}
	return strings.TrimRight(s, " ")
}

// pauseBeforeExit keeps the console window open when ghbt.exe was started by
// double-clicking it on Windows.
func pauseBeforeExit(stdin io.Reader, stdout io.Writer) {
	fmt.Fprint(stdout, "\nPress Enter to exit...")
	_, _ = bufio.NewReader(stdin).ReadString('\n')
}
