# MailCare

English version: [README.md](README.md)

## 1. 概要

MailCare は、監視しているメールアドレスに届くバウンス（配信できなかったことを知らせる通知メール）を原因ごとにまとめ、
送信 IP のブロック、送信ドメイン認証（SPF / DKIM / DMARC）の失敗、メールサーバーの名前解決の不調といった、メールの送信側で対応が必要な問題を知らせるツールです。確認メールを実際に送って届くかを確かめ、送受信の遅延や障害にも気付けるようにします。
Windows / macOS / Linux で動く単一の実行ファイルで、ブラウザから操作します。

### 1.1 主な機能

- **バウンスの監視とグループ化** — 複数のメールアドレスを IMAP で定期的に受信し、バウンスを「送信 IP のブロック」「送信ドメイン認証の失敗」「宛先アドレスが存在しない」などの原因ごとにグループにまとめます。
  DMARC 集計レポートの不合格の記録もまとめます。名前解決の失敗を知らせるバウンスは、その名前を引き直して原因を切り分けます（DNS 検証）。
- **アラート** — 送信側で対応が必要なものだけをアラートとして表示し、宛先側の問題は「対象外」として分けます。
  アラートごとに「未対応 / 対応済 / 無視」を管理でき、対応済・無視にしたものに同じ問題のバウンスが再び届くと、再確認として一覧に戻ります。
  アラートの概要は PDF のレポートとして、元のメール一式とあわせて ZIP としてもダウンロードできます。
- **AI エージェントによる原因分析** — AI エージェント（Codex CLI）が、アラートごとに原因の分析と対応方法の提案を作成します。新しいアラートや新しい種類のバウンスが届いたときは自動で分析します。
- **通知メール** — 対応が必要なアラートの一覧を、通知先に選んだ利用者へメールで送ります。
- **確認メール（送信確認）** — 決まった時刻に各メールアドレスの SMTP サーバーから確認メールを送り、確認用のメールアカウントに届いたかを確かめて、遅延や障害を画面とメールで知らせます。

そのほか、次の機能があります。

- 迷惑メールや偽の配信失敗通知を見分けて、アラートから除きます。
- 受信したメールを画面で閲覧できます。
- 保持日数を過ぎたメールを毎日削除します。通知メールと迷惑メールは IMAP サーバーからも削除できます（元に戻せないため、保存が必要なメールを受け取るアドレスでは削除しない設定にしてください）。
- 管理者と一般利用者を分けられます。画面は日本語 / 英語、ライト / ダークに対応しています。
- 主な操作はコマンドラインからも行えます。

### 1.2 動作要件

- **OS**: Windows / macOS / Linux（64 ビット。amd64 / arm64）。
- **ポート**: Web 画面は 9790 番（変更可）。
- **監視するメールアカウント**: IMAP で接続できること。IMAP サーバーからメールを削除する場合は、削除の権限が必要です。
- **AI エージェントによる原因分析を使う場合**: [Codex CLI](https://github.com/openai/codex) をインストールし、MailCare を実行する OS ユーザーでサインインしておいてください（`codex login`）。
- **通知メールを使う場合**: 送信に使える SMTP サーバー。
- **確認メールを使う場合**: 確認メールを受け取る専用のメールアカウント（IMAP。削除の権限が必要）と、各メールアドレスの SMTP サーバー。
- **DNS 検証**（初期状態で有効）: 外向きの DNS（53 番ポート）。使えない環境では、設定でオフにするか、リゾルバーを変更してください。

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
   | `--check-time` | 同期時刻（`--check-time 06:00 --check-time 12:00` のように複数指定） |
   | `--workers` | 同時実行数（1〜16、既定 2） |
   | `--mail-keep-days` | メールの保持日数（既定 180） |

3. ブラウザで `http://<サーバー>:9790/` を開き、初期設定画面で最初の管理者を作成します。
4. 「設定 → メールアドレス」で監視するメールアドレスを登録し、「接続テスト」で IMAP に接続できることを確認します。
   送信に使う SMTP サーバーも「SMTP サーバー」に入力してください（送信元の判定と確認メールに使います）。
5. 必要に応じて設定します。
   - 「設定 → 一般設定」: 同期時刻、クリーンアップ、送信元と DNS 検証、AI エージェント
   - 「設定 → 通知」: SMTP サーバー、通知先、通知時刻
   - 「設定 → 確認メール」: 確認メールの宛先と確認メールサーバー、送信時刻
   - 「設定 → 利用者」: 利用者の追加と役割

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

停止は `mailcare service stop`、状態の確認は `mailcare service status` です。コマンドは、サービスと同じ OS ユーザー・同じ `--data-dir` で実行してください（例: `sudo -u mailcare /opt/mailcare/mailcare service stop`）。

### 1.4 データとバックアップ

- データはすべて `data/`（`--data-dir` で変更可）に保存されます。パスワードを暗号化する鍵も含まれるため、**`data/` ディレクトリをまるごとバックアップ**してください。
- バージョンアップは、サーバーを停止して実行ファイルを置き換え、再び起動するだけです。データは起動時に自動で更新されます。
- 判定方法が変わるバージョンアップのあとや、送信元・DNS 検証の設定を変えたあとは、ツール画面の「再判定」を一度実行してください。
  再判定で迷惑メールになったバウンスは、次のクリーンアップで IMAP サーバーから削除されることがあります。先に「迷惑」タブを確認してください。
- DNS 検証が有効な間は、同期のたびに、以前に確かめられなかった名前解決の失敗の通知も改めて確かめます。バージョンアップ後の最初の同期では、古い通知も対象になります。

### 1.5 セキュリティ

- MailCare と Codex CLI は、`data/` 以外を読めない専用の権限の低い OS ユーザーで実行してください。
- `data/` ディレクトリは他のユーザーから読めないようにしてください。
- MailCare は暗号化していない HTTP で待ち受けます。ネットワーク越しに使う場合は、前段のリバースプロキシで HTTPS にし、MailCare は `--web-listen 127.0.0.1` でプロキシからだけ受け付けるようにしてください。`/control/` はプロキシに通さないでください。

### 1.6 外部への接続

MailCare は、設定したメールサーバー（IMAP / SMTP）のほかに、次の相手に接続します。

- **DNS 検証**（初期状態で有効）— バウンスで名前解決に失敗した名前などを、設定のリゾルバー（既定は Cloudflare・Google・Quad9）に問い合わせます。送信元から届いた通知に限り、対象ドメインのネームサーバーにも直接問い合わせます。
  問い合わせ先には、このサーバーの IP アドレスと問い合わせた名前が伝わります。避けたい場合は、DNS 検証をオフにするか、「OS のリゾルバーだけを使う」をオンにしてください。
- **AI エージェント** — 分析のために、アラートの概要と見本のバウンスを Codex CLI 経由で OpenAI に送ります。送らない場合は、MailCare を実行する OS ユーザーで Codex CLI にサインインしないでください。

### 1.7 コマンドライン

同期・再判定などのジョブを実行するコマンドは、サーバーが起動していればサーバーに依頼されます。主なコマンド:

| コマンド | 内容 |
| --- | --- |
| `mailcare user create --username admin --role admin` | 利用者を作成する |
| `mailcare mailbox add --address a@example.com --host imap.example.com --username a@example.com` | 監視するメールアドレスを登録する（パスワードは入力を求められる） |
| `mailcare sync --wait` | 今すぐ全メールアドレスを同期する |
| `mailcare schedule set 06:00 12:00 18:00` | 同期時刻を変更する |
| `mailcare settings show` / `mailcare settings set <キー> <値>` | 設定を表示する / 変更する |
| `mailcare reclassify` | 受信済みのメールを判定し直す（再判定） |
| `mailcare cleanup` | クリーンアップを今すぐ実行する |
| `mailcare probe send` / `mailcare probe check` | 確認メールを今すぐ送る / 到着を今すぐ確認する |
| `mailcare jobs list` | ジョブの履歴を表示する |

すべてのコマンドは `mailcare --help` で確認できます。各コマンドとオプションの詳細は [コマンドラインリファレンス](Documents/command-line-reference-ja.md) を参照してください。

### 1.8 同梱フォント

PDF のレポートには、実行ファイルに同梱した BIZ UDPゴシック（Copyright 2022 The BIZ UDGothic Project Authors、[SIL Open Font License 1.1](embedded/fonts/OFL.txt)）と Go Mono（BSD ライセンス）を使います。

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

<http://localhost:9790> でWeb画面が開きます（`service stop` / `status` は同じポートのコントロールエンドポイントへ接続します）。
実行時データ（マスター DB、メールアドレスごとの索引と原文・本文セクション、エージェントの実行ディレクトリ）は実行ファイルと同じ場所の `data/` に作成されます。

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
