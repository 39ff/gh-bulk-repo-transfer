package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

const transferSynopsis = `Usage: ghbt transfer --to OWNER [flags] [REPO...]

Transfer repositories to OWNER (an organization, or a user).

Select repositories in one of two ways:
  * explicitly: REPO arguments and/or --file (one OWNER/NAME, NAME or URL per
    line, '#' starts a comment). A bare NAME belongs to --from (default: you).
  * by filters: --match, --pushed-before, --forks, ... or --all, applied to
    every repository of --from.

The plan is always shown first. Nothing is transferred until you confirm
(or pass --yes). Use --dry-run to only show the plan.

Examples:
  ghbt transfer --to my-archive --file repos.txt --dry-run
  ghbt transfer --to my-archive old-project another-repo
  ghbt transfer --to my-archive --pushed-before 2022-01-01 --forks exclude --archive
`

const (
	statusTransfer = "transfer"
	statusSkip     = "skip"
	statusError    = "error"
)

type planEntry struct {
	Name   string
	Repo   *Repo
	Status string
	Reason string
}

func cmdTransfer(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("transfer", transferSynopsis, stderr)
	var cf commonFlags
	cf.register(fs)
	var ff filterFlags
	ff.register(fs)
	var (
		to, file         string
		all, dryRun, yes bool
		archive          bool
		interval         time.Duration
	)
	fs.StringVar(&to, "to", "", "destination owner: organization (or user) `name` [required]")
	fs.StringVar(&file, "file", "", "read repositories from this file ('-' = stdin)")
	fs.StringVar(&file, "f", "", "same as --file")
	fs.BoolVar(&all, "all", false, "select all repositories of --from (narrow down with filters)")
	fs.BoolVar(&dryRun, "dry-run", false, "only show what would be transferred")
	fs.BoolVar(&yes, "yes", false, "do not ask for confirmation")
	fs.BoolVar(&yes, "y", false, "same as --yes")
	fs.BoolVar(&archive, "archive", false, "archive each repository after transferring it")
	fs.DurationVar(&interval, "interval", 2*time.Second, "pause between transfers (avoids GitHub rate limits)")

	positional, err := parseArgs(fs, args)
	if err != nil {
		return flagErrCode(err)
	}
	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "error: "+format+"\n", a...)
		fmt.Fprintln(stderr, `Run "ghbt transfer -h" for help.`)
		return 2
	}
	if to == "" {
		return usageErr("--to is required")
	}
	filter, err := ff.compile()
	if err != nil {
		return usageErr("%v", err)
	}
	explicit := file != "" || len(positional) > 0
	if explicit && all {
		return usageErr("--all cannot be combined with --file or repository arguments")
	}
	if !explicit && !all && !filter.active {
		return usageErr("nothing selected: pass repositories as arguments, use --file, or select with filters or --all")
	}
	if file == "-" && !yes && !dryRun {
		return usageErr("--yes is required when the list is read from stdin")
	}

	var refs []string
	if file != "" {
		if refs, err = readRepoListFile(file, stdin); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}
	refs = append(refs, positional...)
	if explicit && len(refs) == 0 {
		fmt.Fprintln(stdout, "No repositories listed. Nothing to transfer.")
		return 0
	}

	client, me, err := connect(ctx, cf, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	dest, err := client.GetAccount(ctx, to)
	if err != nil {
		if isNotFound(err) {
			fmt.Fprintf(stderr, "error: destination %q does not exist\n", to)
		} else {
			fmt.Fprintln(stderr, "error:", err)
		}
		return 1
	}
	source := cf.from
	if source == "" {
		source = me.Login
	}

	entries, err := buildPlan(ctx, client, me, dest, source, refs, explicit, filter, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	nTransfer, _, nErr := printPlan(stdout, entries, dest)
	planExit := 0
	if nErr > 0 {
		planExit = 1
	}
	if nTransfer == 0 {
		fmt.Fprintln(stdout, "Nothing to transfer.")
		return planExit
	}
	if dryRun {
		fmt.Fprintln(stdout, "Dry run: nothing was transferred.")
		return planExit
	}
	if !yes {
		prompt := fmt.Sprintf("Transfer %d repositories to %s? [y/N]: ", nTransfer, dest.Login)
		if !confirm(stdin, stdout, prompt) {
			fmt.Fprintln(stdout, "Aborted. Nothing was transferred.")
			return 1
		}
	}

	return executePlan(ctx, client, me, dest, entries, archive, interval, stdout, stderr)
}

// executePlan transfers every entry with status "transfer", prints a summary
// and returns the exit code.
func executePlan(ctx context.Context, client *Client, me, dest *Account, entries []*planEntry, archive bool, interval time.Duration, stdout, stderr io.Writer) int {
	var nTransfer, nSkip, nErr int
	for _, e := range entries {
		switch e.Status {
		case statusTransfer:
			nTransfer++
		case statusSkip:
			nSkip++
		default:
			nErr++
		}
	}
	needsAcceptance := dest.Type != "Organization" && !strings.EqualFold(dest.Login, me.Login)
	var transferred, archiveFailed int
	var failed []string
	attempted := 0
	for _, e := range entries {
		if e.Status != statusTransfer {
			continue
		}
		if attempted > 0 {
			if sleepFn(ctx, interval) != nil {
				break
			}
		}
		attempted++
		fmt.Fprintf(stdout, "[%d/%d] %s -> %s/%s ... ", attempted, nTransfer, e.Repo.FullName, dest.Login, e.Repo.Name)
		if err := client.TransferRepo(ctx, e.Repo.Owner.Login, e.Repo.Name, dest.Login); err != nil {
			fmt.Fprintf(stdout, "FAILED\n    %v\n", err)
			failed = append(failed, e.Repo.FullName)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		transferred++
		if needsAcceptance {
			fmt.Fprintf(stdout, "requested (%s must accept it)\n", dest.Login)
			continue
		}
		fmt.Fprintln(stdout, "ok")
		if archive && e.Repo.Archived {
			fmt.Fprintln(stdout, "    already archived")
		} else if archive {
			if err := archiveAfterTransfer(ctx, client, dest.Login, e.Repo.Name); err != nil {
				fmt.Fprintf(stdout, "    archive failed: %v\n", err)
				archiveFailed++
			} else {
				fmt.Fprintln(stdout, "    archived")
			}
		}
	}

	fmt.Fprintf(stdout, "\nDone: %d transferred, %d failed, %d skipped, %d error(s) before transfer", transferred, len(failed), nSkip, nErr)
	if rest := nTransfer - attempted; rest > 0 {
		fmt.Fprintf(stdout, ", %d not attempted (interrupted)", rest)
	}
	fmt.Fprintln(stdout)
	if archiveFailed > 0 {
		fmt.Fprintf(stdout, "%d repositories were transferred but could not be archived.\n", archiveFailed)
	}
	if len(failed) > 0 {
		fmt.Fprintln(stdout, "Failed:")
		for _, name := range failed {
			fmt.Fprintln(stdout, "  "+name)
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "Interrupted.")
		return 130
	}
	if len(failed) > 0 || archiveFailed > 0 || nErr > 0 {
		return 1
	}
	return 0
}

func buildPlan(ctx context.Context, c *Client, me, dest *Account, source string, refs []string, explicit bool, filter *repoFilter, log io.Writer) ([]*planEntry, error) {
	var entries []*planEntry
	if explicit {
		fmt.Fprintf(log, "Checking %d repositories...\n", len(refs))
		seen := map[string]bool{}
		for _, ref := range refs {
			owner, name, err := parseRepoRef(ref, source)
			if err != nil {
				entries = append(entries, &planEntry{Name: ref, Status: statusError, Reason: err.Error()})
				continue
			}
			repo, err := c.GetRepo(ctx, owner, name)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				reason := err.Error()
				if isNotFound(err) {
					reason = "not found (or the token cannot access it)"
				}
				entries = append(entries, &planEntry{Name: owner + "/" + name, Status: statusError, Reason: reason})
				continue
			}
			key := strings.ToLower(repo.FullName)
			if seen[key] {
				continue
			}
			seen[key] = true
			e := &planEntry{Name: repo.FullName, Repo: repo}
			if !strings.EqualFold(repo.FullName, owner+"/"+name) {
				e.Name = fmt.Sprintf("%s (listed as %s/%s)", repo.FullName, owner, name)
			}
			if filter.active && !filter.keep(repo) {
				e.Status, e.Reason = statusSkip, "excluded by filters"
			}
			entries = append(entries, e)
		}
	} else {
		fmt.Fprintf(log, "Listing repositories of %s...\n", source)
		repos, err := c.ListRepos(ctx, source, me)
		if err != nil {
			return nil, err
		}
		var selected []*Repo
		for i := range repos {
			if filter.keep(&repos[i]) {
				selected = append(selected, &repos[i])
			}
		}
		sortRepos(selected, "name")
		for _, r := range selected {
			entries = append(entries, &planEntry{Name: r.FullName, Repo: r})
		}
	}
	return entries, checkEntries(ctx, c, entries, dest)
}

// planRepos builds a checked plan for repositories that were already fetched.
func planRepos(ctx context.Context, c *Client, repos []*Repo, dest *Account) ([]*planEntry, error) {
	entries := make([]*planEntry, 0, len(repos))
	for _, r := range repos {
		entries = append(entries, &planEntry{Name: r.FullName, Repo: r})
	}
	return entries, checkEntries(ctx, c, entries, dest)
}

// checkEntries runs the pre-transfer checks for entries without a status yet.
func checkEntries(ctx context.Context, c *Client, entries []*planEntry, dest *Account) error {
	for _, e := range entries {
		if e.Status != "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		e.Status, e.Reason = checkTransferable(ctx, c, e.Repo, dest.Login)
	}
	return nil
}

func checkTransferable(ctx context.Context, c *Client, r *Repo, to string) (status, reason string) {
	if strings.EqualFold(r.Owner.Login, to) {
		return statusSkip, "already owned by " + to
	}
	if r.Permissions != nil && !r.Permissions.Admin {
		return statusError, "admin permission on the repository is required"
	}
	existing, err := c.GetRepo(ctx, to, r.Name)
	switch {
	case err == nil && strings.EqualFold(existing.FullName, to+"/"+r.Name):
		return statusSkip, fmt.Sprintf("%s/%s already exists", to, r.Name)
	case err == nil || isNotFound(err):
		// A redirect to another repository does not block the name.
		return statusTransfer, ""
	default:
		return statusError, "checking destination: " + err.Error()
	}
}

func printPlan(w io.Writer, entries []*planEntry, dest *Account) (nTransfer, nSkip, nErr int) {
	fmt.Fprintf(w, "\nDestination: %s (%s)\n\n", dest.Login, strings.ToLower(dest.Type))
	width := 0
	for _, e := range entries {
		width = max(width, len(e.Name))
	}
	for _, e := range entries {
		line := fmt.Sprintf("  %-8s  %-*s  %s", e.Status, width, e.Name, e.Reason)
		fmt.Fprintln(w, strings.TrimRight(line, " "))
		switch e.Status {
		case statusTransfer:
			nTransfer++
		case statusSkip:
			nSkip++
		default:
			nErr++
		}
	}
	fmt.Fprintf(w, "\n%d to transfer, %d skipped, %d error(s)\n", nTransfer, nSkip, nErr)
	return
}

func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprint(out, prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// archiveAfterTransfer archives owner/name, waiting for the asynchronous
// transfer to finish if the repository is not there yet.
func archiveAfterTransfer(ctx context.Context, c *Client, owner, name string) error {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			if serr := sleepFn(ctx, 3*time.Second); serr != nil {
				return serr
			}
		}
		if err = c.ArchiveRepo(ctx, owner, name); err == nil || !isNotFound(err) {
			return err
		}
	}
	return fmt.Errorf("%s/%s did not appear in time: %w", owner, name, err)
}
