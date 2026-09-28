# MailCare

English version: [README.md](README.md)

## 1. 概要

MailCare は、監視しているメールアドレスに届く「配信できませんでした」「配信が遅れています」といった通知メール（バウンス）を集め、
原因ごとにまとめて、メールの送信側で対応が必要なものを知らせるツールです。
Windows / macOS / Linux で動く単一の実行ファイルで、ブラウザから操作します。

### 1.1 主な機能

- **メールアドレスの監視** — 複数のメールアドレスを IMAP で監視し、決まった時刻（既定 6:00 / 12:00 / 18:00）に自動で新しいメールを取り込みます。
- **バウンスの判定とまとめ** — 取り込んだメールからバウンスを見分け、「送信 IP のブロック」「送信ドメインの認証失敗」「宛先アドレスが存在しない」などの原因ごとにまとめます。
  宛先アドレスや相手サーバーが違っても、対応する内容が同じものは 1 つにまとまります。
- **アラート** — 送信側で対応が必要なものだけをアラートとして表示します。宛先側の問題（宛先不明、容量超過など）は「対象外」として分けて表示します。
  アラートごとに「未対応 / 対応済 / 無視」の状態を管理できます。
- **AI による原因分析** — AI エージェント（Codex CLI）が、アラートごとに原因の分析と対応方法の提案を作成します。
  新しいアラートや、既存のアラートに新しい種類の通知が届いたときに自動で分析します。原因を確定できない報告には「要確認」が付きます。
- **メール通知** — 対応が必要なアラートの一覧を、通知先に選んだ利用者へメールで送ります。
  利用者ごとに、その人の言語（日本語 / 英語）とタイムゾーンで送ります。通知の時刻と間隔（毎日〜7 日ごと）を設定できます。
- **メールの閲覧** — 取り込んだメールを、すべて / バウンス / その他に分けて閲覧できます（本文のテキスト / HTML 表示、原文のダウンロード）。
- **自動整理** — 取り込んだメールは保持日数（既定 180 日）を過ぎると自動で削除します。
  対応済・無視にしたアラートのメールを、メールアドレスごとに設定した日数（新規登録時の既定 60 日。0 で削除しない）を過ぎたら IMAP サーバーから削除することもできます。
- **利用者と権限** — 管理者と一般利用者（閲覧のみ）を分けられます。画面は日本語 / 英語、ライト / ダークに対応しています。
- **コマンドライン** — 起動・停止、利用者やメールアドレスの登録、同期などの操作はコマンドラインからも行えます。

### 1.2 動作要件

- **OS**: Windows / macOS / Linux（64 ビット。amd64 / arm64）。
- **ポート**: Web 画面は 9790 番（変更可）。利用者のブラウザからこのポート（またはリバースプロキシ）に届くようにしてください。
- **監視するメールアカウント**: IMAP で接続できること。IMAP サーバーからのメール削除を使う場合は、メールを削除できる権限が必要です。
- **AI による原因分析を使う場合**: [Codex CLI](https://github.com/openai/codex) をインストールし、MailCare を実行する OS ユーザーでサインインしておいてください（`codex login`）。
  Codex CLI が無くても、原因分析以外の機能は動作します。
- **メール通知を使う場合**: 送信に使える SMTP サーバー。

### 1.3 インストールと初期設定

1. 配布されている ZIP（`mailcare_<版>_<OS>_<CPU>.zip`）を展開し、実行ファイル `mailcare`（Windows は `mailcare.exe`）を設置先のディレクトリに置きます。
2. サーバーを起動します。

   ```bash
   mailcare service start
   ```

   主な起動オプション（指定した値は保存され、次回からは省略できます）:

   | オプション | 内容 |
   | --- | --- |
   | `--web-listen` / `--web-port` | 待ち受けるアドレスとポート（既定 `0.0.0.0` / `9790`） |
   | `--data-dir` | データの保存先（既定は実行ファイルと同じ場所の `data/`） |
   | `--check-time` | 自動チェックの時刻（`--check-time 06:00 --check-time 12:00` のように複数指定） |
   | `--workers` | 同時に実行する処理の数（1〜16、既定 2） |
   | `--mail-keep-days` | 取り込んだメールの保持日数（既定 180） |

3. ブラウザで `http://<サーバー>:9790/` を開き、初期設定画面で最初の管理者を作成します。
4. 「設定 → メールアドレス」で監視するメールアドレスを登録し、「接続テスト」で IMAP に接続できることを確認します。
5. 必要に応じて設定します。
   - 「設定 → 一般設定」: チェック時刻、メールの保持日数、AI エージェント（モデル、推論レベル、自動分析の有効 / 無効）
   - 「設定 → 通知」: SMTP サーバーと通知先の利用者、通知の時刻と間隔
   - 「設定 → 利用者」: 利用者の追加と権限

`mailcare service start` はフォアグラウンドで動きます。常駐させる場合は、OS のサービスとして登録してください。Linux（systemd）の例:

```ini
[Unit]
Description=MailCare
After=network-online.target

[Service]
User=mailcare
ExecStart=/opt/mailcare/mailcare service start
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

停止は `mailcare service stop`、状態の確認は `mailcare service status` です。

### 1.4 データとバックアップ

- データはすべて `data/`（`--data-dir` で変更可）に保存されます。取り込んだメール、アラートと分析結果、設定、利用者が含まれます。
- IMAP / SMTP のパスワードを暗号化する鍵も `data/mailcare.db` の中にあります。**`data/` ディレクトリをまるごとバックアップ**してください。
  鍵だけを別に保管する必要はありません。
- バージョンアップは、サーバーを停止して実行ファイルを置き換え、再び起動するだけです。データは起動時に自動で新しい版に合わせて更新されます。

### 1.5 セキュリティ

- MailCare と Codex CLI は、`data/` 以外を読めない専用の権限の低い OS ユーザーで実行してください。
  分析対象のメールは外部から届く信頼できない入力であり、Codex CLI はその OS ユーザーが読めるファイルを読めます。
- `data/` ディレクトリは他のユーザーから読めないようにしてください（権限 0700。そうでない場合は起動時に警告します）。
- MailCare 自身は暗号化していない HTTP で待ち受けます。ネットワーク越しに使う場合は、前段のリバースプロキシで HTTPS にしてください。
  そのうえで、MailCare は `--web-listen 127.0.0.1` でプロキシからだけ受け付けるようにし、プロキシは `Host` ヘッダーを書き換えずに渡してください。

### 1.6 コマンドライン

サーバーが起動していれば、コマンドラインからの処理はサーバーに依頼されます。主なコマンド:

| コマンド | 内容 |
| --- | --- |
| `mailcare user create --username admin --role admin` | 利用者を作成する |
| `mailcare mailbox add --address a@example.com --host imap.example.com --username a@example.com` | 監視するメールアドレスを登録する（パスワードは入力を求められる） |
| `mailcare sync --wait` | 今すぐ全メールアドレスを同期する（取り込み → まとめ → 分析） |
| `mailcare schedule set 06:00 12:00 18:00` | 自動チェックの時刻を変更する |
| `mailcare settings show` / `mailcare settings set <キー> <値>` | 設定を表示する / 変更する |
| `mailcare cleanup` | 保持日数を過ぎたメールなどを今すぐ整理する |
| `mailcare jobs list` | 処理の履歴を表示する |

すべてのコマンドは `mailcare --help` で確認できます。

## 2. 開発者向けリファレンス

### デバッグ用にWSL内でIPを調べるには

```bash
hostname -I | awk '{print $1}'
```

### アイコン生成

```bash
convert frontend/public/icons/app-icon-org.png -define icon:auto-resize=256,128,96,64,48,32,24,16 frontend/public/favicon.ico

convert frontend/public/icons/app-icon-org.png -resize 72x72   frontend/public/icons/icon-72x72.png
convert frontend/public/icons/app-icon-org.png -resize 96x96   frontend/public/icons/icon-96x96.png
convert frontend/public/icons/app-icon-org.png -resize 128x128 frontend/public/icons/icon-128x128.png
convert frontend/public/icons/app-icon-org.png -resize 144x144 frontend/public/icons/icon-144x144.png
convert frontend/public/icons/app-icon-org.png -resize 152x152 frontend/public/icons/icon-152x152.png
convert frontend/public/icons/app-icon-org.png -resize 192x192 frontend/public/icons/icon-192x192.png
convert frontend/public/icons/app-icon-org.png -resize 384x384 frontend/public/icons/icon-384x384.png
convert frontend/public/icons/app-icon-org.png -resize 512x512 frontend/public/icons/icon-512x512.png
convert frontend/public/icons/app-icon-org.png -resize 180x180 frontend/public/icons/apple-touch-icon.png
```

### Go 操作コマンド

デバッグモジュールの追加・更新

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
```

モジュールの追加

```bash
go get <package-name>
```

モジュールの追加・ビルド

```bash
go install <package-name>
```

モジュールファイルの作成

```bash
go mod init <module-name>
```

モジュールのダウンロード（モジュール名を省略するとgo.modの全て）

```bash
go mod download <module-name>
```

モジュールの最適化（ソースとgo.modの双方向での一致）

```bash
go mod tidy
```

モジュールの最新化

```bash
go get -u
```

Go バージョンの更新

```bash
go mod tidy --go=1.26
```

キャッシュのクリア

```bash
go clean --cache --testcache
```

### フロントエンド操作コマンド

依存パッケージのインストール

```bash
cd frontend
yarn install
```

ビルド（`frontend/dist`に出力。Goの`go:embed`が参照するため、`go build`や`go vet`の前に必要）

```bash
cd frontend
yarn build
```

型チェック

```bash
cd frontend
yarn lint
```

整形（`src`配下）

```bash
cd frontend
yarn format
```

Material Iconsフォントの配置（`node_modules`から`public/fonts`へコピー）

```bash
cd frontend
yarn setup:fonts
```

### 起動

```bash
go run . service start
```

http://localhost:9790 でWeb画面が開きます（`service stop` / `status` は同じポートのコントロールエンドポイントへ接続します）。
実行時データ（マスター DB、メールごとの索引と原文・本文セクション、エージェントの実行ディレクトリ）は実行ファイルと同じ場所の `data/` に作成されます。

### Lintとテスト

```bash
make lint
make test
```

### ビルドやリリース方法

ビルド（事前に`cd frontend && yarn build`が必要）

```bash
go build
```

リリース

```bash
make
```
