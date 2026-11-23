# ghbt — GitHub Bulk Transfer

[![CI](https://github.com/ShelterLLC/githubBulkTransfer/actions/workflows/ci.yml/badge.svg)](https://github.com/ShelterLLC/githubBulkTransfer/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ShelterLLC/githubBulkTransfer?sort=semver)](https://github.com/ShelterLLC/githubBulkTransfer/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**English** | [日本語](README.ja.md)

Move the GitHub repositories you no longer need into another organization — an
"archive" org, for example — in one go. Pick them in an interactive terminal UI,
or script it.

<p align="center">
  <img src="docs/demo-interactive.gif" alt="ghbt interactive mode: choose source and destination, tick repositories, review the plan, transfer" width="806">
</p>

- **Interactive**: run `ghbt`, choose *from* (your account or an organization) and
  *to*, tick repositories in a filterable list sorted by last push, confirm.
- **Safe by default**: shows a checked plan before doing anything — name already
  taken in the destination, already moved, or no admin rights are skipped or
  reported, and nothing happens until you confirm. `--dry-run` is available too.
- **Scriptable**: `ghbt list` exports an editable list, `ghbt transfer` takes a
  file, arguments or filters (`--pushed-before`, `--forks`, `--match`, ...).
- **Optional archiving** of each repository after it has been moved.
- Waits and retries automatically when GitHub's rate limits kick in.
- Single binary for Windows, macOS and Linux (amd64 / arm64). Also works with
  GitHub Enterprise Server (`--api-url`).

## Install

Download the archive for your platform from the
[latest release](https://github.com/ShelterLLC/githubBulkTransfer/releases/latest),
extract it and put `ghbt` (`ghbt.exe` on Windows) somewhere on your `PATH`.

| Platform | File |
| --- | --- |
| Windows | `ghbt_<version>_windows_amd64.zip` (`arm64` for ARM) |
| macOS (Apple Silicon) | `ghbt_<version>_darwin_arm64.tar.gz` |
| macOS (Intel) | `ghbt_<version>_darwin_amd64.tar.gz` |
| Linux | `ghbt_<version>_linux_amd64.tar.gz` (`arm64` for ARM) |

On macOS, if the binary is blocked because it is not notarized:

```bash
xattr -d com.apple.quarantine ./ghbt
```

With Go 1.25 or newer you can also install from source:

```bash
go install github.com/ShelterLLC/githubBulkTransfer/cmd/ghbt@latest
```

## Quick start

```bash
gh auth login
```

```bash
ghbt
```

That's it: `ghbt` picks up the [GitHub CLI](https://cli.github.com/) login
(see [Authentication](#authentication) for other options) and walks you through:

1. **Move repositories FROM** — your own account, or an organization you belong
   to. Organizations are labelled `(owner)` / `(member)`.
2. **Move repositories TO** — the destination organization.
3. **Select repositories** — least recently pushed first, with visibility, fork,
   archived and star information.
4. **Archive them after moving?**
5. **Review** the checked plan and choose **Transfer** or **Cancel**.

| Key | Action |
| --- | --- |
| `↑` `↓` (or `k` `j`) | Move |
| `space` / `x` | Select / unselect |
| `/` | Filter by typing (name, date, `fork`, `private`, ...) |
| `ctrl+a` | Select all / none |
| `enter` | Confirm |
| `shift+tab` | Back to the previous question on the same screen |
| `ctrl+c` | Quit (nothing is transferred while you are still choosing) |

On Windows you can also just double-click `ghbt.exe`. If the full-screen UI does
not work in your terminal, use numbered prompts instead:

```bash
ghbt interactive --accessible
```

## Command-line usage

<p align="center">
  <img src="docs/demo-cli.gif" alt="ghbt list and ghbt transfer on the command line" width="806">
</p>

### Edit a list, then transfer it

```bash
ghbt list --sort pushed -o repos.txt
```

`repos.txt` contains one repository per line. Delete the lines (or comment them
out with `#`) of the repositories you want to **keep**:

```text
# 4 of 4 repositories owned by alice (ghbt list, 2026-11-23)
# Keep only the lines of repositories you want to TRANSFER.
# Delete a line (or put '#' in front of it) to leave that repository where it is.
# Then run: ghbt transfer --to <ORG> --file <this file> --dry-run
alice/jquery-playground  # public  fork  pushed:2015-06-12  stars:0
alice/old-blog           # public  pushed:2017-03-21  stars:0  Jekyll blog
alice/ml-experiments     # private  pushed:2019-11-30  stars:0
alice/dotfiles           # public  pushed:2026-10-28  stars:8  My dotfiles
```

```bash
ghbt transfer --to my-archive-org --file repos.txt --dry-run
```

```bash
ghbt transfer --to my-archive-org --file repos.txt
```

### Name repositories directly

```bash
ghbt transfer --to my-archive-org old-project another-repo alice/third-repo
```

`NAME`, `OWNER/NAME`, `https://github.com/OWNER/NAME` and
`git@github.com:OWNER/NAME.git` are all accepted. A bare `NAME` belongs to
`--from` (default: you).

### Select with filters

Without a file or arguments, filters (or `--all`) select from all repositories
of `--from`:

```bash
ghbt transfer --to my-archive-org --pushed-before 2023-01-01 --forks exclude --archive
```

```bash
ghbt transfer --to my-archive-org --match '^test-'
```

```bash
ghbt transfer --to my-archive-org --all --exclude keep
```

The same filters work with `ghbt list`, so you can preview the selection first.

### Commands and flags

```text
ghbt                                       interactive mode
ghbt interactive [flags]                   interactive mode with flags
ghbt list     [flags]                      list repositories (editable text or JSON)
ghbt transfer --to OWNER [flags] [REPO...]  transfer repositories to OWNER
ghbt version                               print the version
```

Common flags:

| Flag | Description |
| --- | --- |
| `--from OWNER` | Source user or organization (default: you) |
| `--token TOKEN` | GitHub token (prefer `GH_TOKEN` or `gh auth login`) |
| `--api-url URL` | API base URL; GitHub Enterprise Server: `https://HOST/api/v3` |

Filters (`list` and `transfer`):

| Flag | Description |
| --- | --- |
| `--match REGEXP` | Only names matching the regular expression (case-insensitive) |
| `--exclude REGEXP` | Skip names matching the regular expression |
| `--visibility all\|public\|private\|internal` | Filter by visibility |
| `--archived any\|only\|exclude` | Include, only, or exclude archived repositories |
| `--forks any\|only\|exclude` | Include, only, or exclude forks |
| `--pushed-before YYYY-MM-DD` | Last pushed before the date (never-pushed ones match too) |

`list`: `-o FILE`, `--sort name|pushed`, `--format text|json`.

`transfer`: `--to OWNER` (required), `--file FILE` / `-f` (`-` = stdin, needs
`--yes`), `--all`, `--dry-run`, `--yes` / `-y`, `--archive`, `--interval 2s`.

`interactive`: `--from OWNER`, `--accessible`, `--interval 2s`.

Exit codes: `0` success, `1` failures or errors, `2` usage error, `130` interrupted.

## Authentication

The first token found is used:

1. `--token`
2. `GH_TOKEN`
3. `GITHUB_TOKEN`
4. the GitHub CLI login (`gh auth token`)
5. interactive mode only: you are asked to paste one (it is not stored)

If you use a personal access token, create a **classic** token with the `repo`
scope (add `read:org` to see your organizations and roles in interactive mode;
`gh auth login` includes both). Fine-grained tokens are limited to a single
owner, so they usually cannot move repositories from your account to an
organization. If the organization uses SAML SSO, authorize the token for it.

Required permissions:

- **admin** on each repository you move
- permission to **create repositories** in the destination organization

## What happens when a repository is transferred

- Issues, pull requests, wiki, stars and watchers move with the repository.
- Web and git URLs of the old location redirect to the new one — until a new
  repository with the same name is created at the old location. GitHub Pages
  URLs are not redirected.
- Repositories whose name already exists in the destination are skipped.
- Transfers to another **user** must be accepted by that user by email.
- To undo, transfer back:
  `ghbt transfer --from my-archive-org --to <your-user> REPO...`

See GitHub's documentation:
[Transferring a repository](https://docs.github.com/en/repositories/creating-and-managing-repositories/transferring-a-repository).

## Development

```bash
go test ./...
```

The tests use a fake GitHub API server, so no token or network is needed. The
demo GIFs are recorded against a fake API as well; see [docs/demo](docs/demo).

### Releasing

[`.github/workflows/release.yml`](.github/workflows/release.yml) builds all
platforms with [GoReleaser](https://goreleaser.com/) and publishes a GitHub
release with checksums. Either push a tag:

```bash
git tag v1.0.0
```

```bash
git push origin v1.0.0
```

or run **Actions → Release → Run workflow** and enter the version.
[`ci.yml`](.github/workflows/ci.yml) tests every push on Linux, macOS and
Windows and uploads snapshot binaries as workflow artifacts.

## License

[MIT](LICENSE). This project is not affiliated with or endorsed by GitHub.
