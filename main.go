// Command ghbt (GitHub Bulk Transfer) moves many GitHub repositories to
// another owner, typically an organization, in one go.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"
)

// Set at build time via -ldflags (see .goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Package-level hooks so tests can run without real waits or environment.
var (
	getenv  = os.Getenv
	nowFn   = time.Now
	sleepFn = func(ctx context.Context, d time.Duration) error {
		if d <= 0 {
			return ctx.Err()
		}
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
)

const usageText = `ghbt - GitHub Bulk Transfer

Move many GitHub repositories to another owner (usually an organization) at once.

Usage:
  ghbt list     [flags]                      List repositories as an editable list
  ghbt transfer --to OWNER [flags] [REPO...]  Transfer repositories to OWNER
  ghbt version                               Print version information

Run "ghbt <command> -h" to see the flags of a command.

Authentication (first one found is used):
  --token flag, $GH_TOKEN, $GITHUB_TOKEN, or "gh auth token" (GitHub CLI login)

Typical workflow:
  ghbt list --sort pushed -o repos.txt
  # edit repos.txt and keep only the repositories you want to move
  ghbt transfer --to my-archive-org --file repos.txt --dry-run
  ghbt transfer --to my-archive-org --file repos.txt
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "list", "ls":
		return cmdList(ctx, args[1:], stdout, stderr)
	case "transfer", "mv":
		return cmdTransfer(ctx, args[1:], stdin, stdout, stderr)
	case "version", "--version", "-version", "-v":
		fmt.Fprintf(stdout, "ghbt %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "help", "--help", "-help", "-h":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

// commonFlags are shared by every command that talks to the API.
type commonFlags struct {
	token  string
	apiURL string
	from   string
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.token, "token", "", "GitHub token (default: $GH_TOKEN, $GITHUB_TOKEN, then 'gh auth token')")
	fs.StringVar(&c.apiURL, "api-url", "", "API base URL (default: $GITHUB_API_URL or "+defaultAPIURL+"; GHES: https://HOST/api/v3)")
	fs.StringVar(&c.from, "from", "", "source owner (user or org) whose repositories are listed (default: you)")
}

// connect resolves the token, builds a client and verifies the credentials.
func connect(ctx context.Context, cf commonFlags, stderr io.Writer) (*Client, *Account, error) {
	apiURL := cf.apiURL
	if apiURL == "" {
		apiURL = getenv("GITHUB_API_URL")
	}
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	token, source, err := resolveToken(ctx, cf.token, apiURL)
	if err != nil {
		return nil, nil, err
	}
	c := NewClient(apiURL, token, stderr)
	me, err := c.CurrentUser(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Fprintf(stderr, "Authenticated as %s (token from %s)\n", me.Login, source)
	return c, me, nil
}

func newFlagSet(name, synopsis string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "%s\nFlags:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parseArgs parses flags and positional arguments in any order
// (the standard flag package stops at the first positional argument).
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// flagErrCode maps a flag parsing error to an exit code.
func flagErrCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}
