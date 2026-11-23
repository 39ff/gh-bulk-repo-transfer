package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/term"
)

const interactiveSynopsis = `Usage: ghbt interactive [flags]      (running "ghbt" with no arguments does the same)

Step by step in the terminal:
  1. choose where to move repositories FROM (you or one of your organizations) and TO
  2. tick the repositories to move (space: select, /: filter, ctrl+a: all)
  3. review the checked plan and confirm
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
		fmt.Fprintln(stderr, named(`error: interactive mode needs a terminal; use "ghbt list" and "ghbt transfer" in scripts`))
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

type stage int

const (
	stageSource stage = iota
	stageSourceName
	stageDest
	stageDestName
	stageRepos
	stageConfirm
	stageDone
)

// wizard is a small state machine: form() builds the question for the
// current stage and advance() acts on the answer (API calls, validation)
// and moves to the next stage. The full-screen UI runs every stage inside a
// single bubbletea program, so no keystrokes are lost between questions.
type wizard struct {
	ctx         context.Context
	in          io.Reader
	out, errOut io.Writer
	accessible  bool

	client *Client
	me     *Account
	orgs   []OrgMembership
	stage  stage

	// answers
	source, sourceName string
	dest, destName     string
	picked             []string
	archive, confirmed bool

	// results
	sourceLogin string
	destAcct    *Account
	candidates  []*Repo
	entries     []*planEntry
	nTransfer   int
	nErr        int
}

func (w *wizard) run(cf commonFlags, interval time.Duration) (int, error) {
	var err error
	if w.client, w.me, err = w.connect(cf); err != nil {
		return 1, err
	}
	if w.orgs, err = w.client.MyOrgs(w.ctx); err != nil {
		fmt.Fprintf(w.errOut, "Could not list your organizations (%v); you can type names instead.\n", err)
	}
	if cf.from != "" { // source given on the command line: start at the destination
		acct, err := w.client.GetAccount(w.ctx, cf.from)
		if isNotFound(err) {
			return 1, fmt.Errorf("owner %q not found", cf.from)
		} else if err != nil {
			return 1, err
		}
		w.sourceLogin, w.stage = acct.Login, stageDest
	}

	if w.accessible {
		err = w.runAccessible()
	} else {
		err = w.runTUI()
	}
	if err != nil {
		return 1, err
	}
	switch {
	case w.nTransfer == 0: // nothing selected, nothing transferable, or no repositories
		return w.planExit(), nil
	case !w.confirmed: // "Cancelled" was already shown
		return 1, nil
	}
	fmt.Fprintln(w.out)
	return executePlan(w.ctx, w.client, w.me, w.destAcct, w.entries, w.archive, interval, w.out, w.errOut), nil
}

func (w *wizard) planExit() int {
	if w.nErr > 0 {
		return 1
	}
	return 0
}

// runAccessible asks one plain-text question after another.
func (w *wizard) runAccessible() (err error) {
	defer func() {
		// huh's plain-text prompts can panic when input ends after an invalid
		// answer (e.g. Ctrl+D); report that as an error instead of crashing.
		if r := recover(); r != nil {
			err = fmt.Errorf("input ended unexpectedly (%v)", r)
		}
	}()
	for w.stage != stageDone {
		f := w.form().WithAccessible(true).WithInput(w.in).WithOutput(w.out)
		if err := f.RunWithContext(w.ctx); err != nil {
			return err
		}
		lines, err := w.advance()
		for _, l := range lines {
			fmt.Fprintln(w.out, l)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// runTUI runs all questions inside one bubbletea program.
func (w *wizard) runTUI() error {
	m := &wizardModel{w: w, spin: spinner.New(spinner.WithSpinner(spinner.Dot))}
	m.form = w.form()
	opts := []tea.ProgramOption{tea.WithContext(w.ctx), tea.WithInput(w.in), tea.WithOutput(w.out)}
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, tea.ErrProgramKilled) {
			return huh.ErrUserAborted
		}
		return err
	}
	if m.aborted {
		return huh.ErrUserAborted
	}
	return m.err
}

type advancedMsg struct {
	lines []string
	err   error
}

type wizardModel struct {
	w        *wizard
	form     *huh.Form
	spin     spinner.Model
	busy     string // non-empty while advance() runs
	size     *tea.WindowSizeMsg
	err      error
	aborted  bool
	finished bool
}

func (m *wizardModel) Init() tea.Cmd { return m.form.Init() }

func (m *wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.size = &msg
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.aborted = true
			return m, tea.Quit
		}
	case spinner.TickMsg:
		if m.busy == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case advancedMsg:
		m.busy = ""
		var cmds []tea.Cmd
		if len(msg.lines) > 0 {
			cmds = append(cmds, tea.Println(strings.Join(msg.lines, "\n")))
		}
		if msg.err != nil || m.w.stage == stageDone {
			m.err, m.finished = msg.err, true
			return m, tea.Sequence(append(cmds, tea.Quit)...)
		}
		m.form = m.w.form()
		cmds = append(cmds, m.form.Init())
		if m.size != nil {
			if _, cmd := m.form.Update(*m.size); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Sequence(cmds...)
	}
	if m.busy != "" || m.finished {
		return m, nil
	}

	fm, cmd := m.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateAborted:
		m.aborted = true
		return m, tea.Quit
	case huh.StateCompleted:
		m.busy = m.w.busyText()
		w := m.w
		return m, tea.Batch(cmd, m.spin.Tick, func() tea.Msg {
			lines, err := w.advance()
			return advancedMsg{lines: lines, err: err}
		})
	}
	return m, cmd
}

func (m *wizardModel) View() tea.View {
	switch {
	case m.finished || m.aborted:
		return tea.NewView("")
	case m.busy != "":
		return tea.NewView(m.spin.View() + " " + m.busy + "\n")
	default:
		return tea.NewView(m.form.View())
	}
}

func (w *wizard) busyText() string {
	switch w.stage {
	case stageDest, stageDestName:
		return "Loading repositories..."
	case stageRepos:
		return "Checking the selected repositories..."
	default:
		return "Checking..."
	}
}

// form builds the question for the current stage.
func (w *wizard) form() *huh.Form {
	var fields []huh.Field
	switch w.stage {
	case stageSource:
		opts := []huh.Option[string]{huh.NewOption(w.me.Login+" (your account)", w.me.Login)}
		for _, o := range w.orgs {
			opts = append(opts, huh.NewOption(o.label(), o.Login))
		}
		opts = append(opts, huh.NewOption("Other (type a name)...", otherOwner))
		if w.source == "" {
			w.source = w.me.Login
		}
		fields = append(fields, huh.NewSelect[string]().
			Title("Move repositories FROM").
			Description("Your own account, or an organization you belong to.").
			Options(opts...).Value(&w.source))

	case stageSourceName:
		w.sourceName = ""
		fields = append(fields, huh.NewInput().Title("Source owner (user or organization name)").
			Value(&w.sourceName).Validate(validateNameInput))

	case stageDest:
		var opts []huh.Option[string]
		for _, o := range w.orgs {
			if !strings.EqualFold(o.Login, w.sourceLogin) {
				opts = append(opts, huh.NewOption(o.label(), o.Login))
			}
		}
		if len(opts) == 0 { // nothing to pick from: ask for a name
			w.stage = stageDestName
			return w.form()
		}
		opts = append(opts, huh.NewOption("Other (type a name)...", otherOwner))
		if w.dest == "" || w.dest == otherOwner || strings.EqualFold(w.dest, w.sourceLogin) {
			w.dest = opts[0].Value
		}
		fields = append(fields, huh.NewSelect[string]().
			Title("Move repositories TO").
			Description(fmt.Sprintf("From %s to:", w.sourceLogin)).
			Options(opts...).Value(&w.dest))

	case stageDestName:
		w.destName = ""
		fields = append(fields, huh.NewInput().Title("Destination organization name").
			Value(&w.destName).Validate(validateNameInput))

	case stageRepos:
		width := 0
		for _, r := range w.candidates {
			width = max(width, len(r.Name))
		}
		width = min(width, 40)
		opts := make([]huh.Option[string], 0, len(w.candidates))
		for _, r := range w.candidates {
			opts = append(opts, huh.NewOption(repoLabel(r, width), r.FullName))
		}
		w.picked = nil
		ms := huh.NewMultiSelect[string]().
			Title(fmt.Sprintf("Select repositories to move to %s (%d, least recently pushed first)", w.destAcct.Login, len(w.candidates))).
			Description("space/x: select   /: filter   ctrl+a: select all   enter: done").
			Options(opts...).
			Height(min(len(opts), 15) + 4).
			Value(&w.picked)
		if !w.accessible { // plain-text mode cannot re-ask once input has ended
			ms.Validate(func(v []string) error {
				if len(v) == 0 {
					return errors.New("select at least one repository with space (or x)")
				}
				return nil
			})
		}
		fields = append(fields, ms, huh.NewConfirm().
			Title("Archive them after moving?").
			Description("Archived repositories become read-only. This can be undone later.").
			Value(&w.archive))

	case stageConfirm:
		what := "Transfer"
		if w.archive {
			what = "Transfer and archive"
		}
		w.confirmed = false
		fields = append(fields, huh.NewConfirm().
			Title(fmt.Sprintf("%s %d repositories to %s?", what, w.nTransfer, w.destAcct.Login)).
			Description("Issues, pull requests, wiki and stars move with them; old URLs redirect.").
			Affirmative("Transfer").
			Negative("Cancel").
			Value(&w.confirmed))
	}
	return huh.NewForm(huh.NewGroup(fields...))
}

func validateNameInput(s string) error {
	if !validName(strings.TrimSpace(s)) {
		return errors.New("enter a valid user or organization name")
	}
	return nil
}

// advance acts on the answer to the current stage and returns lines to show.
func (w *wizard) advance() ([]string, error) {
	switch w.stage {
	case stageSource:
		if w.source == otherOwner {
			w.stage = stageSourceName
			return nil, nil
		}
		w.sourceLogin = w.source
		w.stage = stageDest
		return nil, nil

	case stageSourceName:
		name := strings.TrimSpace(w.sourceName)
		if !validName(name) {
			return nil, errors.New("no valid name given")
		}
		acct, err := w.client.GetAccount(w.ctx, name)
		if isNotFound(err) {
			return []string{fmt.Sprintf("%q does not exist. Try again.", name)}, nil
		}
		if err != nil {
			return nil, err
		}
		w.sourceLogin = acct.Login
		w.stage = stageDest
		return nil, nil

	case stageDest, stageDestName:
		name := w.dest
		if w.stage == stageDestName {
			name = strings.TrimSpace(w.destName)
			if !validName(name) {
				return nil, errors.New("no valid name given")
			}
		} else if name == otherOwner {
			w.stage = stageDestName
			return nil, nil
		}
		acct, err := w.client.GetAccount(w.ctx, name)
		switch {
		case isNotFound(err):
			w.stage = stageDestName
			return []string{fmt.Sprintf("%q does not exist. Try again.", name)}, nil
		case err != nil:
			return nil, err
		case strings.EqualFold(acct.Login, w.sourceLogin):
			w.stage = stageDest
			return []string{"The destination must be different from the source. Try again."}, nil
		}
		w.destAcct = acct
		return w.loadCandidates()

	case stageRepos:
		var selected []*Repo
		for _, r := range w.candidates { // keep the list order
			for _, p := range w.picked {
				if p == r.FullName {
					selected = append(selected, r)
					break
				}
			}
		}
		if len(selected) == 0 {
			w.stage = stageDone
			return []string{"No repositories selected. Nothing was transferred."}, nil
		}
		entries, err := planRepos(w.ctx, w.client, selected, w.destAcct)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		w.nTransfer, _, w.nErr = printPlan(&buf, entries, w.destAcct)
		w.entries = entries
		lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		if w.nTransfer == 0 {
			w.stage = stageDone
			return append(lines, "Nothing to transfer."), nil
		}
		w.stage = stageConfirm
		return lines, nil

	case stageConfirm:
		w.stage = stageDone
		if !w.confirmed {
			return []string{"Cancelled. Nothing was transferred."}, nil
		}
		return nil, nil
	}
	return nil, nil
}

func (w *wizard) loadCandidates() ([]string, error) {
	lines := []string{fmt.Sprintf("From: %s  ->  To: %s", w.sourceLogin, w.destAcct.Login)}
	all, err := w.client.ListRepos(w.ctx, w.sourceLogin, w.me)
	if err != nil {
		return lines, err
	}
	w.candidates = nil
	noAdmin := 0
	for i := range all {
		r := &all[i]
		if r.Permissions != nil && !r.Permissions.Admin {
			noAdmin++
			continue
		}
		w.candidates = append(w.candidates, r)
	}
	if noAdmin > 0 {
		lines = append(lines, fmt.Sprintf("(%d repositories are hidden because you are not an admin of them)", noAdmin))
	}
	if len(w.candidates) == 0 {
		w.stage = stageDone
		return append(lines, fmt.Sprintf("%s has no repositories you can transfer.", w.sourceLogin)), nil
	}
	sortRepos(w.candidates, "pushed")
	w.stage = stageRepos
	return lines, nil
}

// connect uses the usual token sources and asks for a token if none is found.
func (w *wizard) connect(cf commonFlags) (*Client, *Account, error) {
	apiURL := cf.resolvedAPIURL()
	token, source, err := resolveToken(w.ctx, cf.token, apiURL)
	if err != nil {
		var input string
		form := huh.NewForm(huh.NewGroup(
			huh.NewNote().
				Title("GitHub token needed").
				Description("No token found ($GH_TOKEN, $GITHUB_TOKEN or \"gh auth login\").\n"+
					"Create a classic token with the \"repo\" scope at https://github.com/settings/tokens\n"+
					"It is used for this run only and is not saved."),
			huh.NewInput().
				Title("Paste your token").
				EchoMode(huh.EchoModePassword).
				Value(&input),
		)).WithAccessible(w.accessible).WithInput(w.in).WithOutput(w.out)
		if err := form.RunWithContext(w.ctx); err != nil {
			return nil, nil, err
		}
		if token = strings.TrimSpace(input); token == "" {
			return nil, nil, errors.New("no token given")
		}
		source = "prompt"
	}
	return login(w.ctx, apiURL, token, source, w.errOut)
}

func (o OrgMembership) label() string {
	switch o.Role {
	case "admin":
		return o.Login + " (owner)"
	case "member":
		return o.Login + " (member)"
	}
	return o.Login
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
