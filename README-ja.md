# MailCare

English version: [README.md](README.md)

## 1. システム概要

MailCare は、複数のメールアドレスを IMAP で監視し、メールデーモン（MAILER-DAEMON / postmaster など）から届く
「Undelivered Mail Returned to Sender」のような配信不能・遅延の通知メール（バウンス）を集めて、
宛先アドレスや相手サーバーの IP などの揺れを吸収したうえで、
**メールサーバー管理者・ドメイン管理者・宛先アドレスの管理者が対応すべき単位（纏めグループ）** にまとめて表示するツールです。

- **全メールの保持** — 受信したメールは対象外のものも含めて `data/mails/<メールアドレス>/<年>/<月>/`（メールの日付で月ごとに分けます）に原文（`.eml`）と、
  デコード済みの本文セクション（テキストは `<キー>-1.txt`、`<キー>-2.txt`、...、HTML は `<キー>-1.html`、`<キー>-2.html`、...。
  内容のあるパートごとに 1 ファイル、MIME 順）として保存し、ヘッダーなどのメタデータは
  メールアドレスごとの索引 `data/mails/<メールアドレス>.sqlite` だけに持ちます。判定ルールが変わっても、原文から再判定・再索引ができます。
  メールの保持日数は設定できます（既定 180 日。メールの日付から数えます）。保持日数を過ぎたメールは、日付が変わるたびに 1 回実行される
  クリーンアップでファイルと索引の両方から削除され、メールが無くなった纏めグループは分析結果ごと消えます。同じクリーンアップが完了から 30 日を過ぎたジョブ履歴も削除します。
  クリーンアップはツール画面や CLI からも実行できます。
- **対象判定** — 送信者・送信者名・件名・`multipart/report` の配信状態などから、バウンスかどうかと種類（配信不能 / 遅延 / 自動返信）を判定し、
  宛先アドレス、拡張状態コード（`5.1.1` など）、相手側 MTA と IP、診断文を抽出します。
- **纏め** — バウンスをカテゴリ（送信 IP のブロック、送信元アドレスの拒否、送信ドメインの認証失敗、宛先不明、容量超過 など）で判定し、
  管理者が対応する単位（送信サーバーの IP、送信元アドレス、送信ドメイン）と判断主体（ブラックリストの提供元、宛先ドメイン）でグループにまとめます。
  宛先側の問題（宛先不明・容量超過など）は対象外として記録し、アラートには管理者が対応すべきものだけを載せます。
- **エージェント解析** — 端末にインストール済みのエージェント CLI（現在は Codex CLI。追加可能）に原文ファイルのパスを渡し、
  対応対象のグループごとに原因の分析と対応方法の提案を作成して保存します。新着メールで既存グループが増えた場合はそのグループを再解析します。
  解析 1 回ごとの作業ディレクトリ（プロンプト、CLI の出力、報告）は `data/agent/<メールアドレス>/<グループキー>/<報告 ID>/` に作られ、
  設定した保持日数（既定 30 日）を過ぎたものは同じ日次のクリーンアップで削除されます。
- **受信・纏め・解析の分離** — 3 つの処理は独立したジョブで、ツール画面や CLI から個別にも、全アドレス同期として一括でも実行できます。
  同時実行数（ワーカー数）は設定でき、同じ IMAP サーバーのアカウントは自動的に順番に処理されます。
- **定時チェック** — 1 日のチェック時刻（既定 6:00 / 12:00 / 18:00）に自動で全アドレスを同期します。
  アドレス追加後の初回は 90 日、以降は 30 日以内の未取得メールが対象です（いずれもメールの保持日数より前には遡りません）。
- **メール通知** — SMTP を設定すると、通知先に選んだ利用者（利用者ごとに任意で登録したメールアドレス）へ、
  解析済みの対応対象グループの要約・対象メール件数・アラートを開く URL を 1 通にまとめて送ります。通知時刻と通知間隔（毎日〜7 日ごと）を設定できます。
  保存済みの SMTP パスワードは保存したときの接続先にだけ使われ、ホスト・ポート・接続方式・ユーザー名を変えるとき（設定画面、`settings set`、テスト送信）はパスワードの再入力が必要です。
- **Web と CLI** — すべての操作は Web 画面（ポート 9790）から行えます。CLI からはサービスの起動・停止、利用者とトークンの管理、
  メールアドレスの登録、同期・受信・纏め・解析・クリーンアップの実行、再索引、纏めグループの確認ができます（メール本文の閲覧は Web のみ）。トークンは将来の API 用の予約機能です。
- Windows / macOS / Linux で動作する単一バイナリです。

### 1.1 かんたんな使い方

```bash
# サーバーを起動する（ブラウザで http://localhost:9790/ を開き、初期設定画面で最初の管理者を作成する）
mailcare service start

# CLI で管理者を作る場合
mailcare user create --username admin --role admin

# メールアドレスを登録する（パスワードは対話入力できる）
mailcare mailbox add --address bounce@example.com --host imap.example.com --username bounce@example.com

# 今すぐ同期する（受信 → 纏め → 解析。サーバー稼働中はサーバーにジョブを投入する。--wait で完了まで進捗を表示する）
mailcare sync --wait

# 受信だけ / 纏めだけ / 解析だけを実行する
mailcare fetch
mailcare group
mailcare analyze

# チェック時刻を変更する（起動時の --check-time 06:00 --check-time 12:00 ... でも同じ。指定した値は保存され次回から省略できる）
mailcare schedule set 06:00 12:00 18:00
```

Web 画面のナビゲーション:

| メニュー | 内容 |
| --- | --- |
| ダッシュボード（ブランドをクリック） | メールアドレスごとの対応対象グループ数・最終受信、直近の纏めグループ、実行中ジョブ、次回チェック時刻（一般利用者は閲覧のみ） |
| アラート | メールアドレス → 纏めグループ（対応対象 / 対象外の切替、分析報告付き） → 元メール一覧 → メール詳細 |
| メール | メールアドレスごとの生メール閲覧（テキスト / HTML / 原文ダウンロード） |
| ツール | 全アドレス同期、受信のみ、纏めのみ、解析のみ、索引の再作成、判定の再実行、クリーンアップ（保持日数の即時適用）、ジョブ履歴 |
| 設定（管理者のみ） | チェック時刻・メールの保持日数・ワーカー数・エージェント、通知（SMTP・通知先・時刻・間隔）、メールアドレス、利用者、トークン（将来の API 用） |
| 右上の利用者名 | プロファイル（表示名、通知用メールアドレス、言語、タイムゾーン、テーマ、パスワード）、ログアウト |

詳しい設計は [Documents/システム設計書.md](Documents/システム設計書.md)、テーブルは [Documents/テーブル定義.md](Documents/テーブル定義.md)、
エージェントへのプロンプトは [Documents/プロンプト仕様](Documents/プロンプト仕様) を参照してください。

### 1.2 動作要件

- エージェント分析を使う場合は `codex` CLI が PATH にあること（無い場合は分析だけがスキップされ、他の機能は動作します）。
- 実行時データは実行ファイルと同じ場所の `data/` に作成されます（`--data-dir` で変更できます）。中身は次の 3 つだけです。
  - `data/mailcare.db` — マスター DB（利用者、トークン、設定、メールアドレス、ジョブ）。IMAP / SMTP パスワードの暗号化と
    Web セッションの署名に使う秘密鍵もこの中（`settings` テーブル）にあり、鍵ファイルはありません。この 1 ファイルをバックアップすれば鍵も保全されます。
  - `data/mails/` — メールアドレスごとの索引（`<メールアドレス>.sqlite`）と原文ディレクトリ（`.eml` と本文セクションの `.txt` / `.html`）。
  - `data/agent/` — エージェントの実行ごとの作業ディレクトリ（`<メールアドレス>/<グループキー>/<報告 ID>/`）。
- MailCare とエージェント CLI は、`data/` 以外を読めない専用の低権限 OS ユーザーで実行してください。エージェントの読み取り専用サンドボックスは書き込みを防ぐだけで、CLI はその OS ユーザーが読める全ファイル（ホームディレクトリ、鍵、他のアプリの設定など）を読めますし、分析対象のメールは信頼できない入力です。データディレクトリは他のユーザーから読めないようにしてください（権限 0700。そうでない場合はサーバーが起動時に警告します）。ネットワーク越しに使う場合は前段のリバースプロキシで TLS を終端し（サーバー自身は平文 HTTP だけを話します）、セッション Cookie の `Secure` 属性をプロキシで付与するか `web_listen` を `127.0.0.1` にし、プロキシは `Host` ヘッダーを書き換えずに渡してください。

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
