# MailCare コマンドラインリファレンス

English version: [command-line-reference.md](command-line-reference.md)

実行ファイル `mailcare` のすべてのコマンドについて、オプション、値の規則、稼働中のサーバーとの関係を記述する。用語は
Web 画面に合わせる（用語定義: [システム仕様.md](システム仕様.md) 1 章）。`mailcare --help` と `mailcare <コマンド> --help`
はコマンドとオプションの短い説明を表示する。

## 1. 使い方

### 1.1 書式

```text
mailcare [--data-dir DIR] <コマンド> [引数] [オプション]
```

| オプション | 内容 |
| --- | --- |
| `--data-dir DIR` | データディレクトリ。既定は実行ファイルと同じ場所の `data/`。コマンドの前後どちらにも書ける。相対パスは現在のディレクトリから |
| `-h`、`--help` | コマンドのヘルプを表示する（終了コード 0） |

- サーバーと同じ `--data-dir` を指定する（サーバーが指定なしで動いていれば、指定しない）。2 章の動作はこれに依存する。
- データディレクトリとデータベースが無ければ、コマンドが作成する。そのため `--data-dir` を打ち間違えるとエラーにならず、
  空のデータディレクトリができる。使っているディレクトリは `mailcare settings show` の 1 行目（`data_dir`）で確認できる。
- 日時はローカル時刻で表示する。`--json` の出力の日時は RFC 3339（UTC）。
- エラーは標準エラーに `Error: <メッセージ>` と表示し、14 章の終了コードで終わる。コマンドライン自体の誤り（未知のコマンド、
  必須オプションの欠落、選択肢に無い値）は使用法を表示して終了コード 2 で終わる。

### 1.2 コマンドの一覧

| コマンド | 用途 | 章 |
| --- | --- | --- |
| `service start` / `stop` / `status` | サーバーの起動・停止・状態表示 | 3 |
| `user ...` | 利用者の管理 | 4 |
| `token ...` | API トークンの管理（将来の API のための予約機能） | 5 |
| `mailbox ...` | 監視するメールアドレスの管理 | 6 |
| `sync`、`fetch`、`group`、`reindex`、`reclassify`、`analyze`、`notify`、`cleanup`、`probe send`、`probe check` | ジョブを今すぐ実行する | 7 |
| `groups ...` | メールアドレスのグループの一覧・詳細・状態の変更 | 8 |
| `schedule show` / `set` | 同期時刻の表示・設定 | 9 |
| `settings show` / `set` | 設定の表示・変更 | 10 |
| `jobs` / `jobs cancel` | ジョブの履歴の表示、待機中のジョブの取消 | 11 |
| `version` | バージョンの表示 | 12 |

## 2. コマンドと稼働中のサーバー

### 2.1 ジョブのコマンド: サーバーへの依頼とコマンド内での実行

7 章のコマンドはジョブを実行する。投入の前に、同じデータディレクトリのサーバーが稼働しているかを確かめる。

1. 保存された `web_port`（既定 9790）を読み、`http://127.0.0.1:<ポート>/control/status` に問い合わせる。このとき、
   サーバーがデータディレクトリの `control.token` に書いたコントロールトークンを付ける。
2. 応答によって、ジョブを実行する場所が決まる。

| 状況 | 動作 | 表示 |
| --- | --- | --- |
| このデータディレクトリのサーバーが応答した | ジョブをサーバーに投入する | `Server is running on port N; submitting to it.` |
| ポートに何も応答しない | コマンドのプロセス内で最後まで実行する | `Running <ジョブ> in this process...` |
| 別のデータディレクトリのサーバーが応答した | サーバーが無いときと同じく、コマンドのプロセス内で実行する | `A server is running on port N for another data directory; running in this process.` |
| このデータディレクトリのサーバーがコントロールトークンを拒否した | 何も実行せず、終了コード 5 | `... refused the control token of this command: run it as the OS user of the service with the same --data-dir, or restart the server` |

- サーバーに依頼することで、1 つのメールアドレスの索引に書き込むプロセスを常に 1 つにする。
- 同じジョブ（種別・メールアドレス・対象が同じ）が待機中または実行中なら、重ねて投入しない。サーバーはそのジョブを示し
  （`Job #N (...) is already queued` または `... running`）、完了を待つコマンドはそのジョブを追う。コマンドの
  プロセス内で実行するときは、そのようなジョブがあるとエラーで終わる。待機中のジョブは `mailcare jobs cancel ID` で
  取り消せる。

### 2.2 ジョブの完了を待つ

| コマンド | サーバーに依頼したとき | コマンドのプロセス内で実行するとき |
| --- | --- | --- |
| `sync`、`cleanup` | 投入して戻る。`--wait` のときだけ待つ | 最後まで実行する |
| `fetch`、`group`、`reindex`、`reclassify`、`analyze`、`notify`、`probe send`、`probe check` | 常に待つ | 最後まで実行する |

- 待つ間は、各ジョブの進捗と結果（`Job #N done: <結果>`）を表示する。そのジョブが生んだジョブも追う: アドレスを省略した
  コマンドのメールアドレスごとのジョブと、同期・グループ化・索引の再構築・再判定の後に投入される分析。その間に
  スケジューラ・Web 画面・ほかのコマンドが投入したジョブは追わない。
- これらの後の分析は、自動分析が有効（`agent_enabled`）で AI エージェントの CLI がインストールされているときだけ投入される。
- サーバーの処理を待っている間の Ctrl+C は待つのをやめるだけで、ジョブはサーバーで続く
  （`Stopped waiting; the jobs keep running on the server.`、終了コード 1）。コマンドのプロセス内で実行中の Ctrl+C は
  ジョブを中止する（終了コード 1）。
- 追ったジョブのどれかが失敗または取り消されると、終了コード 5 で終わる（`N job(s) failed`）。

### 2.3 投入前の拒否

今は何もできないジョブは、何も投入せずに終了コード 3 で拒否する。

| コマンド | 拒否する条件 |
| --- | --- |
| `analyze` | AI エージェントの CLI（`agent_provider`）がインストールされていない、または記録した利用上限が続いている（メッセージに解除時刻を示す） |
| `probe send` | 確認メールが無効、確認メールサーバーが未設定（確認メールの宛先・IMAP ホスト・ユーザー名・パスワード）、または送信確認がオンの有効なメールアドレスが無い |
| `probe check` | 確認メールが無効、確認メールサーバーが未設定、または未対応の確認メール（到着待ち・遅延・障害・送信失敗）が無い |

スケジューラと、ほかのジョブの後に投入される分析は、この判定を通らない。

### 2.4 そのほかのコマンド

そのほかのコマンド（`user`、`token`、`mailbox`、`groups`、`schedule`、`settings`、`jobs`）は、サーバーの稼働の有無に
関係なく、データディレクトリのデータベースを直接読み書きする。稼働中のサーバーはほとんどの変更を次に使うときに読む。
例外は次のとおり。

- `schedule set` と `settings set check_times`: スケジューラが 30 秒以内に新しい同期時刻を読む。
- `settings set web_listen`、`web_port`、`cookie_ttl_hours`、`workers`: サーバーの次回の起動から反映する（同時実行数は、
  Web の設定画面で変えればすぐに反映する）。

### 2.5 OS ユーザー・ホスト・データディレクトリ

- コントロールエンドポイントはループバックアドレスでだけ応答する。コマンドはサーバーと同じマシンで実行する。
- `control.token` を読めるのは、サーバーを実行している OS ユーザーだけ（権限 0600）。コマンドはその OS ユーザーで、同じ
  `--data-dir` を指定して実行する（例: `sudo -u mailcare /opt/mailcare/mailcare service stop`）。
- 別の OS ユーザーで実行すると、データベースを開けない（終了コード 3）か、サーバーがコントロールトークンを拒否する
  （2.1、終了コード 5。`service stop` と `service status` も同じ）。ジョブのコマンドは、そのデータディレクトリの
  サーバーが稼働している間、コマンドのプロセス内でジョブを実行しない。

## 3. サーバー: service

### 3.1 service start

```text
mailcare service start [--web-listen ADDR] [--web-port N] [--workers N] [--check-time HH:MM ...] [--mail-keep-days N]
```

サーバー（Web 画面、ジョブのワーカー、スケジューラ）をフォアグラウンドで起動する。Ctrl+C または SIGTERM で停止する。

| オプション | 内容 | 既定 |
| --- | --- | --- |
| `--web-listen ADDR` | 待ち受けアドレス: IP アドレスまたはホスト名 | 保存値、無ければ `0.0.0.0` |
| `--web-port N` | 待ち受けポート、1〜65535 | 保存値、無ければ 9790 |
| `--workers N` | 同時実行数、1〜16 | 保存値、無ければ 2 |
| `--check-time HH:MM` | 同期時刻（ローカル時刻）。オプションを繰り返すか、カンマで区切る | 保存値、無ければ `06:00,12:00,18:00` |
| `--mail-keep-days N` | メールの保持日数、1〜3650 | 保存値、無ければ 180 |

- 指定した値は同名の設定（`web_listen`、`web_port`、`workers`、`check_times`、`mail_keep_days`）に保存され、次回からは
  省略できる。待ち受けアドレスとポートはポートを開く前に保存される。これらが原因で起動に失敗したときは、正しい値を
  指定して起動し直す。
- 範囲外または形の不正な値は、サーバーを起動する前に終了コード 2 で終わる。
- 1 つのデータディレクトリにサーバーは 1 つ: 2 つ目の起動は `another MailCare server is running for this data
  directory` で失敗し（終了コード 5）、データベースに触れない。サーバーはデータディレクトリの `server.lock` をロックし、
  停止時にファイルを削除する。異常終了で残ったファイルは次の起動を妨げない。
- 停止は、まずスケジューラと実行中のジョブを止め（そのメールは次の実行で扱う）、処理中の要求に停止を知らせてから、要求（ダウンロードを含む）の終わりを待つ（待つ時間に制限は無い）。待っている間にもう一度 Ctrl+C（SIGINT / SIGTERM）を送ると、待たずにすぐ終了する。
- 待ち受けアドレスが特定のアドレス（`0.0.0.0`、`::`、`127.0.0.1`、`::1`、`localhost` 以外）のときは、2 章のコマンドのために、
  同じポートで `127.0.0.1` でも待ち受ける。`::1` ではコマンドがサーバーに届かない（コマンドは `127.0.0.1` に接続するため）。
- サーバーがデフォルトの API トークンを作成したときは、その値をログに 1 度だけ出す。利用者がまだいなければ、Web の
  初期設定画面か `user create --role admin` で最初の管理者を作成するよう、ログで案内する。
- 常駐させるには OS のサービスとして登録する（[README-ja.md](../README-ja.md) 1.3）。

### 3.2 service stop

```text
mailcare service stop [--web-port N]
```

このデータディレクトリの稼働中のサーバーを停止する。

| オプション | 内容 | 既定 |
| --- | --- | --- |
| `--web-port N` | 稼働中のサーバーのポート | 保存された `web_port`、無ければ 9790 |

- そのポートで別のデータディレクトリのサーバーが動いていれば、停止しない
  （`the server on port N serves another data directory: <dir>`、終了コード 5）。
- ポートにサーバーがいない、またはコントロールトークンを拒否された: 終了コード 5。
- コマンドは、サーバーが要求を受け付けた時点で戻る（`Server shutdown initiated`）。サーバーはその後 `service start` の
  とおりに止まる: スケジューラと実行中のジョブを先に止め、処理中の要求の終わりを待つ。

### 3.3 service status

```text
mailcare service status [--web-port N]
```

稼働中のサーバーの状態を表示する: `status`、`name`、`version`、`web_listen`、`data_dir`、`uptime`、`users`、`tokens`、
`mailboxes`、`active_jobs`、`next_check`（次の同期、ローカル時刻）。`--web-port` は `service stop` と同じ。ポートに
サーバーがいない、またはコントロールトークンを拒否された: 終了コード 5。

## 4. 利用者: user

利用者は Web 画面にログインする人である。ユーザー名は `A-Z a-z 0-9 _ . -` の 1〜64 文字。パスワードは 8 文字以上、
72 バイト以下（13 章）。

### 4.1 user create

```text
mailcare user create --username NAME [--display-name NAME] [--email ADDRESS] [--language ja|en] [--timezone TZ] [--theme auto|light|dark] [--role admin|user] [--password PASSWORD]
```

利用者を作成する。`mailcare user create --username admin --role admin` で、Web の初期設定画面を使わずに最初の管理者を
作成できる。

| オプション | 内容 | 既定 |
| --- | --- | --- |
| `--username NAME` | ユーザー名（必須） | - |
| `--display-name NAME` | 表示名、128 文字まで | ユーザー名 |
| `--email ADDRESS` | 通知メール用のメールアドレス。利用者の間で重複できない | なし |
| `--language ja\|en` | 画面とその利用者へのメールの言語 | `ja` |
| `--timezone TZ` | タイムゾーン、IANA 名（`Asia/Tokyo`、`UTC` など） | `Asia/Tokyo` |
| `--theme auto\|light\|dark` | テーマ | `auto` |
| `--role admin\|user` | 役割: 管理者または一般利用者 | `user` |
| `--password PASSWORD` | パスワード | 入力を求める（13 章） |

### 4.2 user list

```text
mailcare user list [--json]
```

利用者の一覧を表示する: ID、ユーザー名、表示名、メールアドレス、タイムゾーン、役割、最終ログイン。`--json` は言語・
テーマ・作成日時を加える。ID は `notify_user_ids`（10.7）に指定する値である。

### 4.3 user update

```text
mailcare user update USERNAME [--display-name NAME] [--email ADDRESS] [--language ja|en] [--timezone TZ] [--theme auto|light|dark]
```

利用者のプロフィールを変更する。指定しないオプションの項目は変わらず、1 つ以上の指定が必要（無ければ終了コード 2）。
`--display-name ""` はユーザー名に戻し、`--email ""` はメールアドレスを消し、言語・タイムゾーン・テーマの空の値は既定値に
戻す。

### 4.4 user passwd

```text
mailcare user passwd USERNAME [--password PASSWORD]
```

新しいパスワードを設定する（`--password` を省略すると入力を求める）。その利用者の Web のセッションは終了する。

### 4.5 user role

```text
mailcare user role USERNAME --role admin|user
```

役割を変更する。最後の管理者は一般利用者にできない（終了コード 2）。

### 4.6 user delete

```text
mailcare user delete USERNAME [-y]
```

`[y/N]` の確認の後に利用者を削除する。`-y` で確認を省く。最後の管理者は削除できない（終了コード 2）。

## 5. API トークン: token

トークンは将来の API のための予約機能である。どの利用者にも属さず、Web 画面のログインには使えない。値は `mlc_` で始まる。

### 5.1 token create

```text
mailcare token create --identifier ID [--name NAME] [--expires EXPIRY] [--json]
```

トークンを作成し、値を表示する。

| オプション | 内容 | 既定 |
| --- | --- | --- |
| `--identifier ID` | 識別子（必須）: `A-Z a-z 0-9 _ -` の 1〜64 文字、重複不可 | - |
| `--name NAME` | 表示名 | 識別子 |
| `--expires EXPIRY` | 有効期限: 今からの期間（`720h`、`90m`。単位は `h`、`m`、`s`）、RFC 3339 の日時、または `YYYY-MM-DD HH:MM:SS` / `YYYY-MM-DD`（UTC） | 無期限 |
| `--json` | JSON で出力する（値を含む） | - |

トークンが 1 つも無いときに作成したトークンは、デフォルトトークンになる。

### 5.2 token list

```text
mailcare token list [--json]
```

識別子・表示名・デフォルトかどうか・有効期限・作成日時を表示する（値は表示しない）。

### 5.3 token show

```text
mailcare token show ID [--json]
```

1 つのトークンを値とともに表示する。

### 5.4 token delete

```text
mailcare token delete ID [-y]
```

`[y/N]` の確認の後にトークンを削除する。`-y` で確認を省く。デフォルトトークンを削除しても、ほかのトークンはデフォルトに
ならない。サーバーは次回の起動時に、識別子 `default` のトークンをデフォルトにするか、無ければ作成する。

## 6. メールアドレス: mailbox

メールアドレスは、監視するメールアドレスとその IMAP の接続設定である。アドレスは小文字で保存し、コマンドは大文字小文字を
問わずにアドレスでメールアドレスを探す。

### 6.1 mailbox add

```text
mailcare mailbox add --address ADDRESS --host HOST --username USER [--port N] [--security ssl|starttls|none] [--password PASSWORD] [--folder FOLDER] [--initial-days N] [--recent-days N] [--server-keep-days N] [--smtp-host HOST] [--display-name NAME] [--disabled] [送信確認のオプション（6.2）]
```

メールアドレスを登録し、IMAP の接続テストを行う。

| オプション | 内容 | 既定 |
| --- | --- | --- |
| `--address ADDRESS` | 監視するメールアドレス（必須） | - |
| `--host HOST` | IMAP サーバー: ホスト名または IP アドレス（`::1` などの IPv6 も可。`host:port` は不可）（必須） | - |
| `--username USER` | IMAP のユーザー名（必須） | - |
| `--port N` | IMAP のポート、1〜65535 | 993（`starttls` でも同じ） |
| `--security ssl\|starttls\|none` | 接続方式 | `ssl` |
| `--password PASSWORD` | IMAP のパスワード | 入力を求める（13 章） |
| `--folder FOLDER` | 受信するフォルダー | `INBOX` |
| `--initial-days N` | 初回の受信日数: 最初の同期で遡る日数、1〜3650 | 90 |
| `--recent-days N` | 通常の受信日数: 2 回目以降の同期で遡る日数、1〜3650 | 10 |
| `--server-keep-days N` | サーバーでの保持日数、0〜3650（0 = IMAP サーバーから削除しない）。`mail_keep_days` 以下 | 60（`mail_keep_days` の方が短ければその値） |
| `--smtp-host HOST` | このアドレスのメールを送信する SMTP サーバー（送信元の 1 つ） | 未設定 |
| `--display-name NAME` | 表示名（確認メールの差出人名にも使う）、128 文字まで | なし |
| `--disabled` | 監視を無効にして登録する（自動の同期をしない） | 監視は有効 |

- アドレスは新しいものでなければならず、ディレクトリ名に使う形でも重複できない。`a!b@example.com` と `a_b@example.com`
  はどちらも `a_b@example.com` になるので、2 つ目は拒否する。
- `--smtp-host` は、その SMTP サーバーが付ける `Received` ヘッダーで名乗る名前である。英数字・ハイフン・ドットからなり、
  IP アドレスと `localhost` は拒否する。通知メールの SMTP サーバー（10.7）ではない。送信元の判定に使い、送信確認（6.2）も
  このサーバーから送る。
- 受信日数は、メールの保持日数（`mail_keep_days`）より前には遡らない。
- サーバーでの保持日数はメールの日付から数える。これを過ぎた処理対象と迷惑メールは、日次のクリーンアップが IMAP
  サーバーから削除する。削除は元に戻せないため、保存が必要なメールを受け取るアドレスでは 0 にする。
- 接続テストに失敗してもメールアドレスは登録したまま警告を表示する（終了コード 0）。`mailbox update` で設定を直し、
  `mailbox test` で確かめる。

### 6.2 送信確認のオプション（mailbox add と mailbox update）

```text
[--probe on|off] [--smtp-port N] [--smtp-security ssl|starttls|none] [--smtp-username USER] [--smtp-password PASSWORD | --ask-smtp-password]
```

送信確認がオンのアドレスは、確認メールをその SMTP サーバー（`--smtp-host`）から送る。

| オプション | 内容 | 既定 |
| --- | --- | --- |
| `--probe on\|off` | 送信確認のオン / オフ | `add` はオフ、`update` は変更なし |
| `--smtp-port N` | SMTP のポート、1〜65535（587 など）。送信確認がオンなら必須 | - |
| `--smtp-security ssl\|starttls\|none` | 接続方式 | `starttls` |
| `--smtp-username USER` | SMTP のユーザー名。`""` = 認証なし | 認証なし |
| `--smtp-password PASSWORD` | SMTP のパスワード。値はシェルの履歴とプロセス一覧に残る | - |
| `--ask-smtp-password` | SMTP のパスワードの入力を求める（13 章） | - |

- 送信確認がオンなら、`--smtp-host` と `--smtp-port` が必須。
- `--smtp-port`、`--smtp-security`、`--smtp-username`、`--smtp-password`、`--ask-smtp-password` は、コマンドの後に送信確認が
  オンである（今オンにする、または `update` で既にオン）ときだけ指定できる。それ以外は終了コード 2。
- `--probe off` は SMTP サーバーのホスト名だけを残し、ポート・接続方式・ユーザー名・パスワードを消す。
- ユーザー名があるときは、使えるパスワードが必要。`mailbox add --probe on --smtp-username USER` でパスワードのオプションが
  無ければ入力を求める。`update` では、保存済みのパスワードを使えるのは SMTP サーバーのホスト名・ポート・接続方式・
  ユーザー名が変わらない間だけで、どれかを変えるときは `--smtp-password` か `--ask-smtp-password` が必要（無ければ
  終了コード 2 で、何も保存しない）。ユーザー名を空にするとパスワードも消える。
- `--smtp-security none` でユーザー名があると、ユーザー名とパスワードが平文で送られる旨の警告を表示する。
- 送信確認をオフにするか、メールアドレスの監視を無効にすると、そのアドレスの未対応の確認メールは対応済になる。

### 6.3 mailbox list

```text
mailcare mailbox list [--json]
```

メールアドレスの一覧をドメイン、アドレスの順に表示する: ID、アドレス、IMAP サーバー、有効かどうか、最終受信とその状態
（`never`、`ok`、`error`）。`--json` は各メールアドレスのほかの設定を加える（パスワードは含めない。`smtp_password_set`
で SMTP のパスワードの保存の有無が分かる）。

### 6.4 mailbox show

```text
mailcare mailbox show ADDRESS [--json]
```

1 つのメールアドレスを表示する: IMAP の接続、フォルダー、有効かどうか、受信日数、サーバーでの保持日数、`smtp host`
（空なら `(not set)`）、`send check`（`on: port N (接続方式) as ユーザー名` または `off`）、最終受信、最後のエラー。

### 6.5 mailbox update

```text
mailcare mailbox update ADDRESS [--new-address ADDRESS] [--host HOST] [--port N] [--security ssl|starttls|none] [--username USER] [--password PASSWORD | --ask-password] [--folder FOLDER] [--initial-days N] [--recent-days N] [--server-keep-days N] [--smtp-host HOST] [--display-name NAME] [--enabled | --disabled] [送信確認のオプション（6.2）]
```

指定したオプションの項目だけを変更し、ほかは変わらない。オプションは `mailbox add` と同じで、次が加わる。

| オプション | 内容 |
| --- | --- |
| `--new-address ADDRESS` | メールアドレスを変更する |
| `--password PASSWORD` / `--ask-password` | 新しい IMAP のパスワード（指定する、または入力を求める。13 章） |
| `--smtp-host ""` | SMTP サーバーを消す（送信確認がオンなら `--probe off` も必要） |
| `--enabled` / `--disabled` | 監視の有効 / 無効（同時には指定できない） |

- 保存済みの IMAP のパスワードを使えるのは、ホスト・ポート・接続方式・ユーザー名が変わらない間だけ。どれかを変えるときは
  `--password` か `--ask-password` が必要で、無ければ終了コード 2
  （`imap_password is required when the connection settings change`）で何も保存しない。`--smtp-host` だけの変更には
  IMAP のパスワードは要らない。
- `--server-keep-days` は `mail_keep_days` 以下。`mail_keep_days` を後から短くしたために超えている保存値は、変えなければ
  そのまま受け付け、クリーンアップは短い方を使う。
- アドレスを変更すると、原文・索引・エージェントの作業ディレクトリも新しい名前に移す。そのメールアドレスのジョブが
  実行中のとき（終了コード 5）、新しいアドレスをほかのメールアドレスが使っているとき、新しい名前のデータが既にあるときは
  拒否する。
- `--display-name` の空の値は表示名を変えない。`--port`、`--initial-days`、`--recent-days` の 0 は値を変えない。

### 6.6 mailbox delete

```text
mailcare mailbox delete ADDRESS [-y] [--keep-data]
```

`[y/N]` の確認の後にメールアドレスを削除する。`-y` で確認を省く。

- `--keep-data` が無ければ、原文・索引（グループと分析結果を含む）・エージェントの作業ディレクトリも削除する。
  `--keep-data` はこれらをデータディレクトリに残す。
- そのメールアドレスの待機中のジョブは取り消す。ジョブの実行中は削除を拒否する（終了コード 5）。
- そのメールアドレスの確認メールの記録は削除する。ジョブの履歴は残る（11.1）。IMAP サーバーのメールには触れない。

### 6.7 mailbox test

```text
mailcare mailbox test ADDRESS
```

保存した設定で接続し、ログインしてフォルダーを開く。成功なら `ok` を表示し、失敗ならエラーを表示して終了コード 5 で終わる。

## 7. ジョブの実行

これらのコマンドはジョブを今すぐ実行する。稼働中のサーバーに依頼するか、コマンドのプロセス内で実行する（2 章）。
アドレスを省略すると、`sync` と `fetch` は有効なすべてのメールアドレスを、`group`、`reindex`、`reclassify`、`analyze`、
`cleanup` は有効・無効を問わずすべてのメールアドレスを対象にする。アドレスを指定すれば、監視が無効のメールアドレスも処理する。

### 7.1 sync

```text
mailcare sync [ADDRESS ...] [--wait] [--all-time]
```

同期: 新しいメールを受信してグループ化し、分析が必要なグループの分析を投入する。

| オプション | 内容 |
| --- | --- |
| `--wait` | サーバーに依頼したとき、ジョブの完了を待って進捗を表示する（2.2） |
| `--all-time` | 受信日数（メールの保持日数より前には遡らない）ではなく、フォルダー全体を探す |

`--all-time` では、メールの保持日数より古いメールも受信する。次のクリーンアップは、その中の処理対象と迷惑メールを IMAP
サーバーから削除し（サーバーでの保持日数が 0 より大きいとき。6.1）、その後にそれらすべてを手元から削除する。

### 7.2 fetch

```text
mailcare fetch [ADDRESS ...] [--all-time]
```

受信のみ: 新しいメールを保存し、グループ化しない（「グループ化待ち」として残る）。`--all-time` は `sync` と同じ。

### 7.3 group

```text
mailcare group [ADDRESS ...]
```

グループ化のみ: まだグループ化していないメールを判定してグループ化し、分析を投入する（グループの一覧は 8 章の `groups`）。

### 7.4 reindex

```text
mailcare reindex [ADDRESS ...]
```

索引の再構築: 原文から索引を作り直し、すべてのメールの判定とグループ化をやり直して、分析を投入する。再び現れたグループは
状態・対応履歴・分析結果を引き継ぐ。

### 7.5 reclassify

```text
mailcare reclassify [ADDRESS ...]
```

再判定: 受信済みのすべてのメールの判定とグループ化をやり直し、分析を投入する。判定方法が変わるバージョンアップの後と、
送信元・DNS 検証の設定を変えた後に実行する。グループは状態と分析結果を保ち、メールが無くなったグループは分析結果・
対応履歴とともに削除する。迷惑メールになったメールは次のクリーンアップで IMAP サーバーから削除されることがあるので、
先に「迷惑」タブを確認する。

### 7.6 analyze

```text
mailcare analyze [ADDRESS] [--group KEY | --all]
```

1 つのメールアドレス（ADDRESS を省略するとすべてのメールアドレス）のグループを AI エージェントで分析する。

| オプション | 分析するグループ |
| --- | --- |
| （なし） | 分析待ちの対応対象のグループ |
| `--all` | 対応対象のすべてのグループ |
| `--group KEY` | 1 つのグループ。対象外のグループも可（ADDRESS が必要） |

- `--group` と `--all` は同時に指定できない。ADDRESS の無い `--group` は終了コード 2。
- エージェントの CLI がインストールされていない、または利用上限が続いているときは終了コード 3 で拒否する（2.3）。
- ジョブの途中でエージェントが利用上限を知らせたときは、ジョブはエラーで終わり、分析できなかったグループは上限の後の分析に回る（`--group`・`--all` でも同じ。`--group` で指定した対象外のグループも）。

### 7.7 notify

```text
mailcare notify [--test [--to ADDRESS]]
```

オプションなし: 通知メールを今すぐ通知先（`notify_user_ids` のうちメールアドレスのある利用者）へ送る。`notify_enabled` が
無効でも送り、送信を記録する。送るものが無ければ、ジョブは `skipped: <理由>` で終わる（終了コード 0）。

| オプション | 内容 | 既定 |
| --- | --- | --- |
| `--test` | 通知の代わりに、保存した SMTP 設定で短いテストメールを送る | - |
| `--to ADDRESS` | テストメールの宛先（`--test` が必要） | メールアドレスのある通知先の全員に 1 通ずつ |

- `--test` で `--to` が無く、メールアドレスのある通知先がいなければ終了コード 2。
- テストメールの言語とタイムゾーンは、宛先のメールアドレスを持つ利用者のもの。ほかのアドレスなら日本語とサーバーの
  ローカル時刻。

### 7.8 cleanup

```text
mailcare cleanup [ADDRESS ...] [--wait]
```

日次のクリーンアップ（`cleanup_time`）と同じく、保持期間を今すぐ適用する。メールアドレスごとに次の順で行う。

1. 有効で、サーバーでの保持日数が 0 より大きいメールアドレスでは、サーバーでの保持日数とメールの保持日数の短い方を過ぎた
   処理対象と迷惑メールを IMAP サーバーから削除する（元に戻せない）。
2. メールの保持日数（`mail_keep_days`）を過ぎたメールを削除する。メールが無くなったグループは削除する。
3. 書き込みの中断で残った一時ファイル（1 日より古いもの）を削除する。
4. `agent_keep_days` を過ぎたエージェントの作業ディレクトリを削除する。
5. メールの保持日数（最低 7 日）を過ぎた確認メールの記録を削除する。
6. 完了から 30 日を過ぎたジョブと、保持期間を過ぎた DNS 検証の結果・通知元の取得結果を削除する。

`--wait` は `sync` と同じ（2.2）。

### 7.9 probe send / probe check

```text
mailcare probe send
mailcare probe check
```

- `probe send`: 送信確認がオンの有効なすべてのメールアドレスから、確認メールの宛先へ確認メールを今すぐ送る。手動の送信
  （送信時刻なし）として記録する。稼働中のサーバーが遅延判断時間の後に到着を確認する。
- `probe check`: 未対応の確認メールを確認メールサーバーで今すぐ探し、状態を更新する。
- どちらも常に完了を待ち、何もできないときは終了コード 3 で拒否する（2.3）。

## 8. グループ: groups

### 8.1 groups list

```text
mailcare groups [list] ADDRESS [--state open|resolved|ignored] [--recheck] [--scope actionable|excluded|all] [--category CATEGORY] [--json]
```

メールアドレスのグループの一覧を表示する。`list` は省略できる。

| オプション | 内容 | 既定 |
| --- | --- | --- |
| `--state STATE` | この状態のグループだけ | すべての状態 |
| `--recheck` | 再確認待ちの対応済・無視のグループだけ | - |
| `--scope SCOPE` | `actionable`: アラート画面のグループ、`excluded`: 宛先側の問題の未対応のグループ（対象外）、`all`: 両方 | `actionable` |
| `--category CATEGORY` | このカテゴリだけ | すべてのカテゴリ |
| `--json` | 最新の分析結果の要約を含めて JSON で出力する | - |

- 列: KEY、STATE（`(re)` は再確認待ち）、CATEGORY、RESP（対応者区分）、MSGS、LAST SEEN、SEVERITY（最新の完了した
  分析結果のもの）、TITLE。
- カテゴリ: `ip_blocked`、`rate_limited`、`sender_blocked`、`auth_failure`、`content_rejected`、`message_too_large`、
  `server_config`、`dns_resolver_failure`、`unknown_failure`、`dmarc_spf_missing`、`dmarc_dkim_failed`、
  `dmarc_not_authenticated`、`user_unknown`、`mailbox_full`、`mailbox_disabled`、`domain_not_found`、
  `dns_failure_external`、`delivery_delay`。

### 8.2 groups ADDRESS KEY

```text
mailcare groups [list] ADDRESS KEY [--json]
```

1 つのグループを表示する: 表示名、状態と対応内容または判断理由・詳細、カテゴリ、対応単位、判定元、対応対象かどうか、
宛先ドメイン、状態コード、対応者区分、件数、初回・最終日時、分析待ちかどうか、診断テンプレート、宛先・相手の IP・相手の
MTA、対応履歴（新しい順）、最新の完了した分析結果。メール本文は表示しない。8.1 の絞り込みのオプションは使わない。

### 8.3 groups set-state

```text
mailcare groups set-state ADDRESS KEY open|resolved|ignored [--reason CODE] [--note TEXT]
```

アラート画面と同じく、グループの状態を変更する。対応履歴には変更者を空として記録する。

| オプション | 内容 |
| --- | --- |
| `--reason CODE` | 対応内容（`resolved`）または判断理由（`ignored`）。どちらでも必須 |
| `--note TEXT` | 詳細: 行ったこと（`resolved`）、または言葉で書いた理由（`ignored` で `--reason other`） |

| 対応内容（`resolved`） | 意味 |
| --- | --- |
| `delisting` | ブラックリストの解除を申請した |
| `dns_fixed` | 送信ドメインの DNS 設定（SPF・DKIM・DMARC）を修正した |
| `server_fixed` | 送信サーバーの設定を修正した |
| `sender_changed` | 差出人アドレスを見直した |
| `content_changed` | 送信内容や添付ファイルを見直した |
| `volume_adjusted` | 送信量や送信速度を調整した |
| `recipient_fixed` | 宛先アドレスを修正または削除した |
| `recipient_asked` | 宛先ドメインの管理者に連絡した |
| `other` | その他 |

| 判断理由（`ignored`） | 意味 |
| --- | --- |
| `temporary` | 一時的な問題で、すでに解消している |
| `recipient_side` | 宛先側の問題 |
| `input_error` | 宛先アドレスの入力誤り |
| `stopped_sending` | この宛先への送信をすでに停止している |
| `spoofing` | 監視ドメインを騙った第三者による送信 |
| `external_service` | 送信に利用している外部サービス側の問題 |
| `false_positive` | 誤検知 |
| `low_impact` | 影響が小さい |
| `test_mail` | テストまたは検証用のメール |
| `other` | その他 |

- `open` は `--reason` と `--note` を使わず、グループの理由と詳細を消す（対応履歴には残る）。
- `ignored` の `--note` は `--reason other` のときだけ保存する。
- 再確認待ちのグループに `resolved` または `ignored` を設定し直すと、新しい判断として記録する。

## 9. 同期時刻: schedule

### 9.1 schedule show

```text
mailcare schedule show [--json]
```

同期時刻と次の同期を表示する（ローカル時刻。同期はサーバーの稼働中だけ行う）。

### 9.2 schedule set

```text
mailcare schedule set [HH:MM ...]
```

同期時刻（ローカル時刻）を置き換える。`H:MM` とカンマ区切りも受け付け、時刻は並べ替えて重複を除く。時刻を指定しなければ
自動の同期を行わない。`none` という語はここでは受け付けない（終了コード 2）。`settings set check_times none` なら受け付ける。
稼働中のサーバーは 30 秒以内に新しい時刻を読む。

## 10. 設定: settings

### 10.1 settings show

```text
mailcare settings show [--json]
```

10.3〜10.8 のすべての設定の実際の値（保存値、無ければ既定値）と、設定できない次の値を表示する。

| 値 | 内容 |
| --- | --- |
| `data_dir` | コマンドのデータディレクトリ |
| `smtp_password`、`probe_imap_password` | `(set)` または `(not set)`。JSON では `smtp_password_set`、`probe_imap_password_set`。パスワードは表示しない |
| `effective_base_url` | `public_base_url` が空の間に通知メールで使う URL |
| `notify_last_sent_at`、`next_send_at` | 最後に送った通知、次の定時の通知（通知日に当たる通知時刻のうち次のもの。通知が無効なら無し） |
| `sending_source_mailbox_hosts` | メールアドレスの SMTP サーバー（`mailbox update --smtp-host` で設定する） |
| `sending_source_mailboxes_without_host` | テキストでは `mailboxes without a host name`。`sending_source_hosts` が空のときの、SMTP サーバーの無いメールアドレス。これらのアドレスでは、送信元以外から送信されたメールのバウンスを見分けられない |
| `probe_last_send_at`、`probe_next_check_at` | 最後の確認メールの送信、次の到着確認 |
| `probe_last_check_error`、`probe_last_delete_error` | 到着確認の最後のエラー（テキストの出力だけ。あるときだけ） |
| `cleanup_last_run_date` | 最後の日次のクリーンアップの日付 |
| `providers` | JSON だけ: エージェントのプロバイダーと、それぞれの CLI がインストールされているか |

テキストの出力では、`agent_provider` に `(available)` または `(not available)` を添え、`dns_check_system_resolver_only` が
有効な間は `dns_check_resolvers` に `(not used: only the OS resolver is queried)` を添える。

### 10.2 settings set

```text
mailcare settings set KEY [VALUE]
```

1 つの設定を変更する。

- VALUE は必須。ただし `smtp_password` と `probe_imap_password` は、省略すると入力を求める（13 章）。
- 空の VALUE（`""`）は既定値に戻す。既定値もほかの値と同じ検証を通るので、ほかの設定との規則に反する既定値は拒否する
  （たとえば通知が有効な間の `smtp_host ""`）。
- 値は Web の設定画面と同じく検証する。拒否した値は終了コード 2 で終わり、何も保存しない。
- 真偽値: `1` / `0`、`true` / `false`、`yes` / `no`、`on` / `off`。一覧: カンマ区切りで、保存済みの一覧を置き換える。
- 未知のキーは既知のキーの一覧を添えて拒否する（終了コード 2）。10.1 の値と内部用の `secret_key` は設定できない。
- 保存した値（`KEY = VALUE`）または `KEY restored to its default` を表示する。

### 10.3 サーバー

| キー | 値 | 既定 |
| --- | --- | --- |
| `web_listen` | 待ち受けアドレス: IP アドレス（IPv6 は角括弧の有無を問わない）またはホスト名 | `0.0.0.0` |
| `web_port` | 待ち受けポート、1〜65535 | 9790 |
| `workers` | 同時実行数、1〜16 | 2 |
| `cookie_ttl_hours` | 「ログイン状態を保持する」でログインしたときの有効時間（時間）。正の整数 | 720 |

- これらはサーバーの次回の起動から反映する（2.4）。
- サーバーの稼働中に `web_port` を変えたら、ジョブのコマンドを実行する前にサーバーを再起動する。それまでジョブのコマンドは
  新しいポートでサーバーを探し、コマンドのプロセス内でジョブを実行してしまう。稼働中のサーバーには
  `service stop --web-port <前のポート>` で届く。

### 10.4 スケジュール・保持期間・再確認

| キー | 値 | 既定 |
| --- | --- | --- |
| `check_times` | 同期時刻: カンマ区切りの `HH:MM`（ローカル時刻）。`none` = 自動の同期なし | `06:00,12:00,18:00` |
| `cleanup_time` | 日次のクリーンアップの時刻: `HH:MM` 1 つ（ローカル時刻） | `02:00` |
| `mail_keep_days` | メールの保持日数、1〜3650 | 180 |
| `recheck_days` | カテゴリごとの再確認までの日数: カンマ区切りの `カテゴリ=日数`、1〜365 日 | カテゴリごと（下記） |

- `mail_keep_days` を短くしても、メールアドレスごとのサーバーでの保持日数は変わらない。クリーンアップは短い方を使う。
- `recheck_days`: 対応済のグループは、対応済にしてからこの日数より後に送ったメールの通知が届くと再確認に戻る（対応が
  反映されるまでの期間）。指定しないカテゴリは値が変わらず、`""` はすべてのカテゴリを既定値に戻す。既定値は `ip_blocked` と
  `sender_blocked` が 14 日、`auth_failure`・`content_rejected`・`message_too_large`・`server_config`・
  `dns_resolver_failure` が 3 日、それ以外が 7 日。カテゴリの一覧は 8.1。

### 10.5 送信元と DNS 検証

| キー | 値 | 既定 |
| --- | --- | --- |
| `sending_source_ipv4` | 送信元 IPv4: IPv4 アドレスと CIDR | なし |
| `sending_source_ipv6` | 送信元 IPv6: IPv6 アドレスと CIDR | なし |
| `sending_source_hosts` | 送信元ホスト名 | なし |
| `dns_check_enabled` | DNS 検証の有効 / 無効 | 有効 |
| `dns_check_resolvers` | リゾルバー: IPv4 / IPv6 アドレス、10 個まで。`none` = 空の一覧 | `1.1.1.1,8.8.8.8,9.9.9.9` |
| `dns_check_system_resolver_only` | OS のリゾルバーだけを使う | 無効 |

- 送信元の一覧はそれぞれ 500 件までで、Web 画面と同じく正規化する（IP は正規の表記、CIDR はネットワークアドレスに揃え、
  1 アドレスだけの CIDR はアドレスとして保存し、ホスト名は小文字にして末尾のドットを除く）。ゾーン付き・ループバック・
  未指定のアドレスと、IP アドレスの形・`_` を含む・`localhost` のホスト名は拒否する。`""` は一覧を空にする。
- メールアドレスの SMTP サーバーも送信元になる。`settings show` で表示し、`mailbox update --smtp-host` で変更する。
- DNS 検証の 3 つのキーは、ほかの 2 つの保存値と組み合わせて検証する。DNS 検証が有効で OS のリゾルバーだけを使わないなら、
  リゾルバーが 1 つ以上必要（無ければ終了コード 2 で、何も保存しない）。カンマと空白だけの値は拒否し、`none` で空の一覧を
  保存する。
- 送信元や DNS 検証の設定を変えた後は、`mailcare reclassify`（7.5）を実行する。

### 10.6 AI エージェント

| キー | 値 | 既定 |
| --- | --- | --- |
| `agent_provider` | エージェント CLI のプロバイダー（現在の登録は `codex`） | `codex` |
| `agent_model` | CLI に渡すモデル。`""` = CLI の既定のモデル | CLI の既定のモデル |
| `agent_reasoning_effort` | CLI に渡す推論レベル。`""` = CLI の設定に従う | CLI の設定 |
| `agent_enabled` | 自動分析の有効 / 無効 | 有効 |
| `agent_keep_days` | 作業ディレクトリの保持日数、1〜365 | 30 |

- `agent_provider` を変えると、`agent_model` と `agent_reasoning_effort` は消える。
- `agent_model` は、形が不正なとき、または保存済みの推論レベルをそのモデルが受け付けないときに拒否する（先に
  `agent_reasoning_effort` を変える）。
- `agent_reasoning_effort` は、形が不正なとき、プロバイダーが推論レベルに対応しないとき、保存済みのモデルが受け付けない
  ときに拒否する。

### 10.7 通知と SMTP

| キー | 値 | 既定 |
| --- | --- | --- |
| `smtp_host` | 通知メールの SMTP ホスト: ホスト名または IP アドレス | なし |
| `smtp_port` | 1〜65535 | 587 |
| `smtp_security` | `ssl`、`starttls`、`none` | `starttls` |
| `smtp_username` | SMTP のユーザー名。`""` = 認証なし | なし |
| `smtp_password` | SMTP のパスワード（暗号化して保存）。VALUE を省略すると入力を求める | なし |
| `smtp_from` | 差出人アドレス | なし |
| `public_base_url` | 公開 URL（メール内のリンクの元）: クエリとフラグメントの無い `http` / `https` の絶対 URL | `http://<web_listen または localhost>:<web_port>` |
| `notify_enabled` | 通知の有効 / 無効 | 無効 |
| `notify_time` | 通知時刻: カンマ区切りの `HH:MM`（ローカル時刻）。通知日にはそれぞれの時刻に送る。`""` = 既定 | `09:00,17:00` |
| `notify_interval_days` | 通知間隔（日）、1〜7 | 1 |
| `notify_user_ids` | 通知先: カンマ区切りの利用者 ID（`user list`）、100 人まで | なし |

- `notify_enabled` を有効にできるのは `smtp_host` と `smtp_from` があるときだけで、有効な間はどちらも空にできない。
- 通知日は、まだ通知を送っていない日、最後に通知を送った日、またはその日から `notify_interval_days` 日以上たった日。
  通知日には `notify_time` のそれぞれの時刻に通知を送る。
- `smtp_password` の前に `smtp_username` が必要。ユーザー名がある間の `smtp_password ""` は拒否する。認証をやめるには
  `settings set smtp_username ""` を実行する（パスワードも消える）。
- パスワードの保存中に `smtp_host`、`smtp_port`、`smtp_security`、`smtp_username` を別の空でない値に変えるときは、
  パスワードが改めて必要になる。コマンドが入力を求め、両方をまとめて保存する。パスワードが保存されていない状態で
  ユーザー名を設定するときも、パスワードの入力を求める。
- `public_base_url` の末尾の `/` は除く。

### 10.8 確認メール

| キー | 値 | 既定 |
| --- | --- | --- |
| `probe_enabled` | 確認メールの有効 / 無効 | 無効 |
| `probe_address` | 確認メールの宛先 | なし |
| `probe_imap_host` | 確認メールサーバー: IMAP のホスト名または IP アドレス（`host:port` は不可） | なし |
| `probe_imap_port` | 1〜65535 | 993 |
| `probe_imap_security` | `ssl`、`starttls`、`none` | `ssl` |
| `probe_imap_username` | IMAP のユーザー名 | なし |
| `probe_imap_password` | IMAP のパスワード（暗号化して保存）。VALUE を省略すると入力を求める。`""` で消す | なし |
| `probe_imap_folder` | フォルダー（改行を含まない） | `INBOX` |
| `probe_send_times` | 送信時刻: カンマ区切りの `HH:MM`（ローカル時刻）。`none` = 定時の送信なし | `07:00,11:00,15:00,19:00` |
| `probe_delay_minutes` | 遅延判断時間（分）、1〜10080 | 15 |
| `probe_failure_minutes` | 障害判断時間（分）、1〜10080 | 60 |
| `probe_subject_tag` | 識別子: 英数字と `.`、`_`、`-` の 1〜16 文字 | `MC` |

- `probe_enabled` を有効にできるのは、確認メールの宛先・IMAP ホスト・ユーザー名・パスワードがそろっているときだけで、
  有効な間はこの 4 つを空にできない。無効にすると、未対応の確認メールは対応済になる。
- 確認メールの宛先には監視するメールアドレスを使わない（確認メールがそのアドレスのメールとして受信される）。
- 送信時刻は 24 個まで、互いに 15 分以上空ける（日をまたぐ間隔も）。遅延判断時間は送信時刻の最も短い間隔（送信時刻が 1 つ
  なら 24 時間）より短く、障害判断時間は遅延判断時間より長くする。
- パスワードの保存中は、IMAP のホスト・ポート・接続方式・ユーザー名の変更を拒否する（入力は求めない）。手順:
  `settings set probe_enabled 0`、`settings set probe_imap_password ""`、接続の変更、`settings set probe_imap_password`
  （入力を求める）、`settings set probe_enabled 1`。
- 確認メールサーバーを共有する MailCare どうしは、別の識別子にする。
- `probe_imap_security none` でユーザー名かパスワードがあると、平文で送られる旨の警告を表示する。
- 確認メールサーバーのアカウントには、メールを削除する権限が必要。

## 11. ジョブの履歴: jobs

### 11.1 jobs list

```text
mailcare jobs [list] [--limit N] [--json]
```

ジョブの履歴を新しい順に表示する（`--limit`、既定 20。`list` は省略できる）。

- 列: ID、KIND、MAILBOX、STATUS、CREATED、RESULT / ERROR。
- 種別: `sync`、`fetch`、`group`、`analyze`、`reindex`、`reclassify`、`notify`、`cleanup`、`probe_send`、`probe_check`。
  状態: `queued`、`running`、`done`、`error`、`canceled`。
- MAILBOX は、メールアドレスごとにジョブを投入するジョブを `(all)`、その後に削除されたメールアドレスのジョブを
  `(deleted mailbox)` と表示する。
- `--json` は対象（target）、`parent_id`（そのジョブを投入したジョブ。直接投入されたジョブは `null`）、進捗、
  `requested_by`（`cli`、`scheduler`、`web:<ユーザー名>`）、開始・終了日時を加える。

### 11.2 jobs cancel

```text
mailcare jobs cancel ID
```

待機中のジョブを取り消す。サーバーの稼働の有無は問わない。実行中・終了したジョブは取り消せない（終了コード 5）。
無い ID は終了コード 2。

## 12. version

```text
mailcare version
```

バージョンを表示する（開発用のビルドは `dev`）。

## 13. パスワードの入力

- オプションの値として渡したパスワード（`--password`、`--smtp-password`）は、シェルの履歴とプロセス一覧に残る。省略するか、
  `--ask-password` / `--ask-smtp-password` を使って入力する。
- 入力を求めるのは次のとき: `--password` の無い `user create` と `user passwd`、`--password` の無い `mailbox add`（IMAP）、
  `mailbox update --ask-password`、`--ask-smtp-password`、SMTP のパスワードの無い `mailbox add --probe on --smtp-username USER`、
  VALUE の無い `settings set smtp_password` と `settings set probe_imap_password`、パスワードが必要な `settings set` での
  SMTP の接続の変更（10.7）。
- どの入力も 2 回求めて照合する。端末では入力を表示しない。パイプからの入力では 1 回の入力が 1 行なので、パスワード 1 つに
  2 行を渡す（例: `printf '%s\n%s\n' "$PW" "$PW" | mailcare user passwd admin`）。
- 2 回の入力が違う、または 2 行目が無い: 終了コード 2。`settings set smtp_password` / `probe_imap_password` の入力が空:
  終了コード 2（保存済みのパスワードは `settings set KEY ""` で消す）。
- 利用者のパスワードは 8 文字以上、72 バイト以下。メールサーバーのパスワードは、データベースに置いた鍵で暗号化して保存する。
  `settings show` と `mailbox show` は保存の有無だけを示す。

## 14. 終了コード

| コード | `app/modules/errors.go` の名前 | 意味 | 例 |
| --- | --- | --- | --- |
| 0 | `ExitSuccess` | 成功 | ヘルプの表示、接続テストに失敗した `mailbox add`、`skipped` で終わったジョブも含む |
| 1 | `ExitGeneral` | 分類されないエラー | ジョブのコマンドの Ctrl+C、想定外のデータベースのエラー |
| 2 | `ExitArgument` | 引数・値の誤り | コマンドラインの誤り、拒否された値、存在しない利用者・メールアドレス・トークン・グループ・ジョブ、一致しないパスワードの入力 |
| 3 | `ExitConfig` | 設定・データベースのエラー | データベースを開けない、投入前の拒否（2.3）、値以外の理由で保存できない設定 |
| 4 | `ExitAuth` | 認証・認可の失敗 | 定義のみ。現在これを返すコマンドは無い |
| 5 | `ExitExec` | サーバー・実行のエラー | サーバーを起動できない（同じデータディレクトリのサーバーが稼働中を含む）、`service stop` / `status` でサーバーがいない、コントロールトークンの拒否、ジョブの失敗、`mailbox test` の失敗、ジョブの実行中のメールアドレスの変更・削除、待機中でないジョブの `jobs cancel` |
| 6 | `ExitFileIO` | ファイル・ディレクトリのエラー | データディレクトリを作成できない、メールアドレスの索引を開けない（`groups`） |
| 7 | `ExitTimeout` | 時間切れ | 定義のみ。現在これを返すコマンドは無い |
