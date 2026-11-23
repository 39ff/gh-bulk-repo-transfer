# githubBulkTransfer (`ghbt`)

いらない GitHub リポジトリを、指定した Organization へまとめて移動（Transfer）するための CLI ツールです。
GitHub REST API の [Transfer a repository](https://docs.github.com/en/rest/repos/repos#transfer-a-repository) を順番に呼び出します。

- Windows / macOS / Linux（amd64・arm64）対応の単一バイナリ（ランタイムのインストール不要）
- **対話モード**: `ghbt` と打つだけで、移動元・移動先を選び、リポジトリをチェックボックスで選んで移動できます
- スクリプト向けに、一覧をテキストファイルに書き出し → 残したい行だけ消す → 一括移動、という流れも使えます
- 実行前に必ず「何を移動するか」を表示し、確認してから移動します（`--dry-run` あり）
- 移動先に同名リポジトリがある / 既に移動済み / 権限がない などは事前チェックでスキップ
- GitHub のレートリミットに当たった場合は自動で待って再試行
- 移動後にアーカイブ（`--archive`）も可能

## インストール

[Releases](../../releases) から OS に合ったファイルをダウンロードして展開し、`ghbt`（Windows は `ghbt.exe`）を PATH の通った場所に置いてください。

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

Go がある環境なら、ソースからビルドもできます。

```bash
go build -o ghbt .
```

## 認証（トークン）

次の順で最初に見つかったものを使います。

1. `--token` オプション
2. 環境変数 `GH_TOKEN`
3. 環境変数 `GITHUB_TOKEN`
4. [GitHub CLI](https://cli.github.com/) のログイン情報（`gh auth token`）
5. （対話モードのみ）どれも無ければ、画面でトークンの入力を求めます（保存はしません）

いちばん簡単なのは GitHub CLI でログインしておく方法です。

```bash
gh auth login
```

Personal Access Token を使う場合は **classic** トークンで `repo` スコープを付けてください。
（fine-grained トークンは 1 つのオーナーにしかアクセスできないため、個人 → Org の移動には向きません。）
移動先 Org が SAML SSO を使っている場合は、トークンの SSO 認可も必要です。
対話モードで所属 Org を一覧から選ぶには `read:org` スコープも必要です（`gh auth login` のトークンには最初から付いています）。無い場合は「Other (type a name)...」を選んで Org 名を入力してください。

必要な権限:

- 移動元リポジトリの **admin** 権限
- 移動先 Org で **リポジトリを作成できる** 権限（Org の owner、または member にリポジトリ作成が許可されていること）

## 使い方

### いちばん簡単: 対話モード

引数なしで実行するだけです（Windows なら `ghbt.exe` をダブルクリックしても起動します）。

```bash
ghbt
```

画面の指示に従って進めます。

1. **Move repositories FROM**: 移動元を選ぶ（自分 / 所属 Org / 名前を入力）
2. **Move repositories TO**: 移動先の Org を選ぶ
3. **Select repositories**: 移動するリポジトリにチェックを付ける（最終 push が古い順に並びます）
4. **Archive them after moving?**: 移動後にアーカイブするか
5. 事前チェックの結果（同名あり・権限なし等はスキップ）を見て、**Transfer** で実行

```text
┃ Select repositories to move to archive-org (4, least recently pushed first)
┃ space/x: select   /: filter   ctrl+a: select all   enter: done
┃ > ✓ old-fork  2019-02-03    [fork]
┃   • bar       2024-01-01    [private]
┃   ✓ foo       2024-01-01    old experiment
┃   • keep-me   2024-01-01    [stars:12]  important project
```

| キー | 動作 |
| --- | --- |
| `↑` `↓`（または `k` `j`） | 移動 |
| `space` / `x` | チェックの ON/OFF |
| `/` | 文字を入力して絞り込み（名前・日付・`fork`・`private` などで絞れます） |
| `ctrl+a` | 全部選択 / 全部解除 |
| `enter` | 決定 |
| `shift+tab` | 同じ画面の前の項目に戻る |
| `ctrl+c` | 選択中なら中止（何も移動しません）。移動の実行中に押すと、残りの移動を中断します |

画面の表示が崩れる環境では、番号を入力して選ぶシンプルなモードも使えます。

```bash
ghbt interactive --accessible
```

### ファイルで指定する流れ（スクリプト・大量向け）

```bash
# 1. 自分のリポジトリ一覧をファイルに書き出す（最終 push が古い順）
ghbt list --sort pushed -o repos.txt
```

`repos.txt` はこんな形式です。

```text
# 5 of 5 repositories owned by alice (ghbt list, 2026-11-23)
# Keep only the lines of repositories you want to TRANSFER.
# Delete a line (or put '#' in front of it) to leave that repository where it is.
# Then run: ghbt transfer --to <ORG> --file <this file> --dry-run
alice/empty      # public  pushed:never  stars:0
alice/old-fork   # public  fork  pushed:2020-05-01  stars:0
alice/old-thing  # public  pushed:2020-05-01  stars:0
alice/bar        # private  pushed:2024-01-01  stars:0
alice/keep-me    # public  pushed:2024-01-01  stars:0
```

2. エディタで開き、**移動したいリポジトリの行だけ残します**。残したい（移動しない）ものは行を消すか、先頭に `#` を付けてください。

```bash
# 3. 何が起きるかだけ確認（実際には移動しない）
ghbt transfer --to my-archive-org --file repos.txt --dry-run

# 4. 実行（確認プロンプトで y を入力）
ghbt transfer --to my-archive-org --file repos.txt
```

実行例:

```text
Destination: my-archive-org (organization)

  transfer  alice/foo
  transfer  alice/bar
  skip      alice/baz      my-archive-org/baz already exists
  error     alice/missing  not found (or the token cannot access it)

2 to transfer, 1 skipped, 1 error(s)
Transfer 2 repositories to my-archive-org? [y/N]: y
[1/2] alice/foo -> my-archive-org/foo ... ok
[2/2] alice/bar -> my-archive-org/bar ... ok

Done: 2 transferred, 0 failed, 1 skipped, 1 error(s) before transfer
```

### 引数で直接指定

```bash
ghbt transfer --to my-archive-org old-project another-repo alice/third-repo
```

リポジトリは `NAME`、`OWNER/NAME`、`https://github.com/OWNER/NAME`、`git@github.com:OWNER/NAME.git` のどれでも指定できます。
`NAME` だけの場合は `--from`（省略時は自分）のリポジトリとして扱います。

### 条件で一括指定

ファイルや引数を指定しない場合は、フィルタ条件（または `--all`）で `--from` の全リポジトリから選びます。

```bash
# 2023年以降 push していない、fork 以外のリポジトリを移動して、移動後にアーカイブ
ghbt transfer --to my-archive-org --pushed-before 2023-01-01 --forks exclude --archive

# 名前が "test-" で始まるものを移動
ghbt transfer --to my-archive-org --match '^test-'

# 全部移動（"keep" を含む名前は除外）
ghbt transfer --to my-archive-org --all --exclude keep
```

同じフィルタは `ghbt list` でも使えるので、先に `list` で対象を確認するのがおすすめです。

### コマンドとオプション

```text
ghbt                                       対話モード
ghbt interactive [flags]                   対話モード（--accessible, --interval などを指定する場合）
ghbt list     [flags]                      リポジトリ一覧を表示 / ファイル出力
ghbt transfer --to OWNER [flags] [REPO...]  リポジトリを OWNER へ移動
ghbt version                               バージョン表示
```

共通オプション:

| オプション | 説明 |
| --- | --- |
| `--from OWNER` | 一覧の取得元（ユーザーまたは Org）。省略時は自分 |
| `--token TOKEN` | GitHub トークン（環境変数や `gh` の方が安全です） |
| `--api-url URL` | API の URL。GitHub Enterprise Server なら `https://HOST/api/v3` |

フィルタ（`list` / `transfer` 共通）:

| オプション | 説明 |
| --- | --- |
| `--match REGEXP` | 名前が正規表現に一致するものだけ（大文字小文字無視） |
| `--exclude REGEXP` | 名前が正規表現に一致するものを除外 |
| `--visibility all\|public\|private\|internal` | 公開範囲で絞り込み |
| `--archived any\|only\|exclude` | アーカイブ済みを含める / だけ / 除外 |
| `--forks any\|only\|exclude` | fork を含める / だけ / 除外 |
| `--pushed-before YYYY-MM-DD` | 指定日より前が最終 push のもの（一度も push されていないものも含む） |

`list` のオプション:

| オプション | 説明 |
| --- | --- |
| `-o FILE` | ファイルに出力 |
| `--sort name\|pushed` | 並び順（`pushed` は最終 push が古い順） |
| `--format text\|json` | 出力形式 |

`transfer` のオプション:

| オプション | 説明 |
| --- | --- |
| `--to OWNER` | 移動先（必須）。通常は Org 名 |
| `--file FILE`, `-f` | 一覧ファイル（`-` で標準入力。その場合は `--yes` 必須） |
| `--all` | `--from` の全リポジトリを対象にする |
| `--dry-run` | 計画を表示するだけで移動しない |
| `--yes`, `-y` | 確認プロンプトを出さない |
| `--archive` | 移動後にアーカイブする |
| `--interval 2s` | 移動ごとの待ち時間（レートリミット対策） |

終了コード: `0` 成功 / `1` 失敗やエラーあり / `2` 引数の誤り / `130` Ctrl+C で中断

## 注意事項

- Issue・PR・Wiki・Star・Watcher などはリポジトリと一緒に移動します。
- 旧 URL（Web と git の clone/push URL）は新しい場所へリダイレクトされます。ただし旧オーナー側に同名リポジトリを新しく作るとリダイレクトは切れます。GitHub Pages の URL はリダイレクトされません。
- 移動先に同名リポジトリがあるものはスキップします（名前を変えてから再実行してください）。
- 移動先がユーザー（自分以外）の場合は、相手がメールで承認するまで移動は完了しません。
- 元に戻したいときは `ghbt transfer --from my-archive-org --to <自分のユーザー名> REPO...` で戻せます。
- 詳細は GitHub のドキュメント [Transferring a repository](https://docs.github.com/en/repositories/creating-and-managing-repositories/transferring-a-repository) を参照してください。

## リリース方法（GitHub Actions）

`.github/workflows/release.yml` が [GoReleaser](https://goreleaser.com/) で 6 種類（Windows / macOS / Linux × amd64 / arm64）のバイナリをビルドし、GitHub Release を作成します。チェックサム（`checksums.txt`）と変更履歴も自動で付きます。

方法 1: タグを push する

```bash
git tag v1.0.0
```

```bash
git push origin v1.0.0
```

方法 2: GitHub の **Actions → Release → Run workflow** からバージョン（例: `v1.0.0`）を入力して実行する（タグは自動で作られます）

`.github/workflows/ci.yml` は push / PR ごとに Windows・macOS・Linux でテストし、スナップショットのバイナリを Actions の Artifacts（`ghbt-snapshot`、14 日保存）としてアップロードします。

## 開発

```bash
go test ./...
```

テストはモックの GitHub API サーバーを使うので、トークンやネットワークは不要です。
