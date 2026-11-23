# gh-bulk-repo-transfer

[![CI](https://github.com/39ff/gh-bulk-repo-transfer/actions/workflows/ci.yml/badge.svg)](https://github.com/39ff/gh-bulk-repo-transfer/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/39ff/gh-bulk-repo-transfer?sort=semver)](https://github.com/39ff/gh-bulk-repo-transfer/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[English](README.md) | **日本語**

使わなくなった GitHub リポジトリを、別の Organization（たとえば「アーカイブ用 Org」）へまとめて移動（Transfer）する [GitHub CLI](https://cli.github.com/) 拡張機能です。単体のツールとしても使えます。
ターミナル上の対話画面で選んで移動することも、コマンドやスクリプトで一括処理することもできます。

<p align="center">
  <img src="docs/demo-interactive.gif" alt="gh bulk-repo-transfer の対話モード: 移動元と移動先を選び、リポジトリにチェックを付け、確認して移動" width="900">
</p>

- **対話モード**: 移動元（自分のアカウント / 所属 Org。owner / member を表示）と移動先を選び、最終 push が古い順に並んだ一覧からチェックを付けて移動できます
- **安全設計**: 実行前に必ず事前チェック済みの計画を表示します。移動先に同名リポジトリがある・移動済み・admin 権限がない、などはスキップまたはエラーとして表示し、確認するまで何も移動しません（`--dry-run` もあります）
- **スクリプト向け**: `list` で編集できる一覧を出力し、`transfer` でファイル・引数・条件（`--pushed-before`、`--forks`、`--match` など）を指定して移動できます
- 移動後に**アーカイブ**することもできます
- GitHub のレートリミットに当たったら自動で待って再試行します
- Windows / macOS / Linux（amd64・arm64）対応。GitHub Enterprise Server でも使えます（`--api-url`）

GitHub CLI 本体にはリポジトリを移動するコマンドがありません（`gh repo` でできるのはアーカイブ・名前変更・削除などで、移動はできません）。この拡張機能はそこを補います。

## インストール

### GitHub CLI 拡張機能として（おすすめ）

```bash
gh extension install 39ff/gh-bulk-repo-transfer
```

```bash
gh bulk-repo-transfer
```

`gh` のログイン情報をそのまま使うので、ほかに設定は要りません。更新は `gh extension upgrade bulk-repo-transfer` です。

### 単体のバイナリとして

`gh` を使わない場合は、[最新のリリース](https://github.com/39ff/gh-bulk-repo-transfer/releases/latest)から OS に合ったファイルをダウンロードして展開し、`ghbt`（Windows は `ghbt.exe`）を PATH の通った場所に置いてください。
`ghbt` は同じプログラムです。この README の `gh bulk-repo-transfer` を `ghbt` に読み替えて使えます。

| OS | ファイル |
| --- | --- |
| Windows | `ghbt_<version>_windows_amd64.zip`（ARM 機は `arm64`） |
| macOS (Apple Silicon) | `ghbt_<version>_darwin_arm64.tar.gz` |
| macOS (Intel) | `ghbt_<version>_darwin_amd64.tar.gz` |
| Linux | `ghbt_<version>_linux_amd64.tar.gz`（ARM 機は `arm64`） |

macOS で「開発元を検証できません」と出る場合は、隔離属性を外してください。

```bash
xattr -d com.apple.quarantine ./ghbt
```

Go 1.25 以上があれば、自分でビルドしてインストールすることもできます。

```bash
go install github.com/39ff/gh-bulk-repo-transfer/cmd/ghbt@latest
```

## すぐに使う

```bash
gh bulk-repo-transfer
```

次の順に進みます。

1. **Move repositories FROM**: 移動元を選びます。自分のアカウントか、所属している Organization（`(owner)` / `(member)` を表示し、owner の Org を先に並べます）
2. **Move repositories TO**: 移動先の Organization を選びます
3. **Select repositories**: 移動するリポジトリにチェックを付けます（最終 push が古い順。公開範囲・fork・アーカイブ済み・スター数も表示）。自分が admin のリポジトリだけが表示されます
4. **Archive them after moving?**: 移動後にアーカイブするか
5. 事前チェックの結果を確認し、**Transfer**（実行）か **Cancel**（中止）を選びます

| キー | 動作 |
| --- | --- |
| `↑` `↓`（または `k` `j`） | 移動 |
| `space` / `x` | チェックの ON/OFF |
| `/` | 文字を入力して絞り込み（名前・日付・`fork`・`private` など） |
| `ctrl+a` | 全部選択 / 全部解除 |
| `enter` | 決定 |
| `shift+tab` | 同じ画面の前の項目に戻る |
| `ctrl+c` | 中止（選んでいる途中なら何も移動しません） |

画面の表示が崩れるターミナルでは、番号で選ぶシンプルなモードを使ってください。

```bash
gh bulk-repo-transfer interactive --accessible
```

単体バイナリの場合、Windows では `ghbt.exe` をダブルクリックしても起動できます。

## コマンドラインで使う

<p align="center">
  <img src="docs/demo-cli.gif" alt="コマンドラインでの list と transfer" width="900">
</p>

### 一覧ファイルを編集して移動する

```bash
gh bulk-repo-transfer list --sort pushed -o repos.txt
```

`repos.txt` は 1 行に 1 リポジトリです。**残したい**（移動しない）リポジトリの行を消すか、先頭に `#` を付けてください。

```text
# 4 of 4 repositories owned by alice (gh bulk-repo-transfer list, 2026-11-23)
# Keep only the lines of repositories you want to TRANSFER.
# Delete a line (or put '#' in front of it) to leave that repository where it is.
# Then run: gh bulk-repo-transfer transfer --to <ORG> --file <this file> --dry-run
alice/jquery-playground  # public  fork  pushed:2015-06-12  stars:0
alice/old-blog           # public  pushed:2017-03-21  stars:0  Jekyll blog
alice/ml-experiments     # private  pushed:2019-11-30  stars:0
alice/dotfiles           # public  pushed:2026-10-28  stars:8  My dotfiles
```

```bash
gh bulk-repo-transfer transfer --to my-archive-org --file repos.txt --dry-run
```

```bash
gh bulk-repo-transfer transfer --to my-archive-org --file repos.txt
```

### リポジトリを直接指定する

```bash
gh bulk-repo-transfer transfer --to my-archive-org old-project another-repo alice/third-repo
```

`NAME`、`OWNER/NAME`、`https://github.com/OWNER/NAME`、`git@github.com:OWNER/NAME.git` のどれでも指定できます。`NAME` だけの場合は `--from`（省略時は自分）のリポジトリとして扱います。

### 条件で選ぶ

ファイルや引数を指定しない場合は、条件（または `--all`）で `--from` の全リポジトリから選びます。

```bash
gh bulk-repo-transfer transfer --to my-archive-org --pushed-before 2023-01-01 --forks exclude --archive
```

```bash
gh bulk-repo-transfer transfer --to my-archive-org --match '^test-'
```

```bash
gh bulk-repo-transfer transfer --to my-archive-org --all --exclude keep
```

同じ条件は `list` でも使えるので、先に対象を確認できます。

### コマンドとオプション

```text
gh bulk-repo-transfer                                       対話モード
gh bulk-repo-transfer interactive [flags]                   対話モード（オプション付き）
gh bulk-repo-transfer list     [flags]                      リポジトリ一覧（編集用テキスト / JSON）
gh bulk-repo-transfer transfer --to OWNER [flags] [REPO...]  リポジトリを OWNER へ移動
gh bulk-repo-transfer version                               バージョン表示
```

共通オプション:

| オプション | 説明 |
| --- | --- |
| `--from OWNER` | 移動元のユーザーまたは Organization（省略時は自分） |
| `--token TOKEN` | GitHub トークン（`GH_TOKEN` や `gh auth login` の方が安全です） |
| `--api-url URL` | API の URL。GitHub Enterprise Server なら `https://HOST/api/v3` |

条件（`list` / `transfer` 共通）:

| オプション | 説明 |
| --- | --- |
| `--match REGEXP` | 名前が正規表現に一致するものだけ（大文字小文字は区別しない） |
| `--exclude REGEXP` | 名前が正規表現に一致するものを除外 |
| `--visibility all\|public\|private\|internal` | 公開範囲で絞り込み |
| `--archived any\|only\|exclude` | アーカイブ済みを含める / だけ / 除外 |
| `--forks any\|only\|exclude` | fork を含める / だけ / 除外 |
| `--pushed-before YYYY-MM-DD` | 指定日より前が最終 push のもの（一度も push されていないものも含む） |

`list`: `-o FILE`、`--sort name|pushed`、`--format text|json`

`transfer`: `--to OWNER`（必須）、`--file FILE` / `-f`（`-` で標準入力。その場合は `--yes` が必要）、`--all`、`--dry-run`、`--yes` / `-y`、`--archive`、`--interval 2s`

`interactive`: `--from OWNER`、`--accessible`、`--interval 2s`

終了コード: `0` 成功 / `1` 失敗やエラーあり / `2` 引数の誤り / `130` 中断

## 認証

次の順で最初に見つかったトークンを使います。

1. `--token`
2. `GH_TOKEN`
3. `GITHUB_TOKEN`
4. GitHub CLI のログイン情報（`gh auth token`）。拡張機能として使う場合は通常これです
5. 対話モードのみ: どれも無ければ画面で入力を求めます（保存はしません）

Personal Access Token を使う場合は **classic** トークンに `repo` スコープを付けてください（対話モードで所属 Org と権限を一覧表示するには `read:org` も必要です。`gh auth login` のトークンには両方付いています）。fine-grained トークンは 1 つのオーナーにしかアクセスできないため、通常は個人アカウントから Organization への移動に使えません。Organization が SAML SSO を使っている場合は、トークンの SSO 認可も必要です。

必要な権限:

- 移動する各リポジトリの **admin** 権限
- 移動先 Organization で**リポジトリを作成できる**権限

## 移動するとどうなるか

- Issue・PR・Wiki・Star・Watcher もリポジトリと一緒に移動します
- 旧 URL（Web と git の URL）は新しい場所へリダイレクトされます。ただし旧オーナー側に同じ名前のリポジトリを作るとリダイレクトは切れます。GitHub Pages の URL はリダイレクトされません
- 移動先に同じ名前のリポジトリがあるものはスキップします
- 移動先が（自分以外の）**ユーザー**の場合は、相手がメールで承認するまで移動は完了しません
- 元に戻すときは逆向きに移動します: `gh bulk-repo-transfer transfer --from my-archive-org --to <自分のユーザー名> REPO...`

詳しくは GitHub のドキュメント [Transferring a repository](https://docs.github.com/en/repositories/creating-and-managing-repositories/transferring-a-repository) を参照してください。

## 開発

```bash
go test ./...
```

テストは GitHub API を真似たモックサーバーを使うので、トークンやネットワークは不要です。README の GIF もモック API とサンプルデータで録画しています（[docs/demo](docs/demo) 参照）。

### リリース方法

リリースは自動です。`main` へ push されたとき（PR をマージしたとき）にコードの変更が含まれていれば、[`.github/workflows/release.yml`](.github/workflows/release.yml) が次のバージョンのタグを付け、[GoReleaser](https://goreleaser.com/) でビルドして GitHub Release を作成します。添付されるのは、単体版のアーカイブ、`gh extension install` がダウンロードする実行ファイル（`gh-bulk-repo-transfer_<tag>_<os>-<arch>`）、チェックサムです。

- 通常は patch 版が上がります（`v1.2.3` → `v1.2.4`）。PR のタイトル（またはコミットメッセージ）に `#minor` / `#major` と書くと、その部分が上がります。最初のリリースは `v0.1.0`（`#major` 付きなら `v1.0.0`）です
- Markdown・`docs/`・`.github/`・`LICENSE` だけの変更ではリリースしません
- 手動でリリースしたいときは **Actions → Release → Run workflow**（バージョン指定も可）を実行するか、`v*` タグを push してください。`v1.0.0-rc.1` のように `-` が付いたタグはプレリリースになり、`gh extension install` の対象になりません

[`ci.yml`](.github/workflows/ci.yml) は push ごとに Linux・macOS・Windows でテストし、スナップショットのバイナリを Artifacts にアップロードします。

## ライセンス

[MIT](LICENSE)。このプロジェクトは GitHub 社とは関係ありません。
