package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// readRepoListFile reads repository references from a file ("-" = stdin).
func readRepoListFile(path string, stdin io.Reader) ([]string, error) {
	if path == "-" {
		return readRepoList(stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	refs, err := readRepoList(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return refs, nil
}

// readRepoList reads repository references, one or more per line.
// Everything after '#' is a comment; blank lines are ignored.
func readRepoList(r io.Reader) ([]string, error) {
	var refs []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\uFEFF") // UTF-8 BOM (e.g. Windows Notepad)
			first = false
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		refs = append(refs, strings.Fields(line)...)
	}
	return refs, sc.Err()
}

var validNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// parseRepoRef accepts NAME, OWNER/NAME, https://github.com/OWNER/NAME[...]
// or git@github.com:OWNER/NAME.git.
func parseRepoRef(ref, defaultOwner string) (owner, name string, err error) {
	s := strings.TrimSpace(ref)
	isURL := false
	switch {
	case strings.Contains(s, "://"):
		u, perr := url.Parse(s)
		if perr != nil {
			return "", "", fmt.Errorf("invalid repository URL %q", ref)
		}
		s, isURL = u.Path, true
	case strings.HasPrefix(s, "git@"):
		if i := strings.IndexByte(s, ':'); i >= 0 {
			s, isURL = s[i+1:], true
		}
	}
	if isURL {
		s = strings.Trim(s, "/")
	}
	parts := strings.Split(s, "/")
	if isURL && len(parts) > 2 {
		parts = parts[:2] // e.g. https://github.com/o/r/tree/main
	}
	switch len(parts) {
	case 1:
		owner, name = defaultOwner, parts[0]
	case 2:
		owner, name = parts[0], parts[1]
	default:
		return "", "", fmt.Errorf("invalid repository %q (expected OWNER/NAME or NAME)", ref)
	}
	name = strings.TrimSuffix(name, ".git")
	if !validName(owner) || !validName(name) {
		return "", "", fmt.Errorf("invalid repository %q (expected OWNER/NAME or NAME)", ref)
	}
	return owner, name, nil
}

func validName(s string) bool {
	return s != "" && s != "." && s != ".." && validNameRe.MatchString(s)
}
