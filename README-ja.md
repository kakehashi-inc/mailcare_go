# MailCare

English version: [README.md](README.md)

## 1. システム概要

MailCare は、複数のメールアドレスを IMAP で監視し、メールデーモン（MAILER-DAEMON / postmaster など）から届く
「Undelivered Mail Returned to Sender」のような配信不能・遅延の通知メール（バウンス）を集めて、
宛先アドレスや相手サーバーの IP などの揺れを吸収したうえで、
**メールサーバー管理者・ドメイン管理者・宛先アドレスの管理者が対応すべき単位（纏めグループ）** にまとめて表示するツールです。

- **全メールの保持** — 受信したメールは対象外のものも含めて `data/mails/<メールアドレス>/` に原文（`.eml`）・テキスト（`.txt`）・
  HTML（`.html`）・解析結果（`.json`）として保存し、索引はメールアドレスごとの `data/mails/<メールアドレス>.sqlite` に持ちます。
  判定ルールが変わっても、原文から再判定・再索引ができます。
- **対象判定** — 送信者・送信者名・件名・`multipart/report` の配信状態などから、バウンスかどうかと種類（配信不能 / 遅延 / 自動返信）を判定し、
  宛先アドレス、拡張状態コード（`5.1.1` など）、相手側 MTA と IP、診断文を抽出します。
- **纏め** — 宛先ドメイン・状態コード・正規化した診断文が同じものを 1 つのグループにまとめ、
  対応すべき立場（送信側サーバー / 宛先アドレスの管理者 / 宛先ドメイン）を機械的に推定します。
- **エージェント分析** — 端末にインストール済みのエージェント CLI（現在は Codex CLI。追加可能）に原文ファイルのパスを渡し、
  グループごとに原因の分析と対応方法の提案を作成して保存します。
- **定時チェック** — 1 日のチェック時刻（既定 6:00 / 12:00 / 18:00）に自動でメールを取り込みます。
  アドレス追加後の初回は 90 日、以降は 30 日以内の未取得メールが対象です。
- **Web と CLI** — すべての操作は Web 画面（ポート 9790）から行えます。CLI からはサービスの起動・停止、利用者とログイントークンの管理、
  メールアドレスの登録、メールチェック、再索引、分析の実行、纏めグループの確認ができます（メール本文の閲覧は Web のみ）。
- Windows / macOS / Linux で動作する単一バイナリです。

### 1.1 かんたんな使い方

```bash
# サーバーを起動する（ブラウザで http://localhost:9790/ を開き、初期設定画面で最初の管理者を作成する）
mailcare service start

# CLI で管理者を作る場合
mailcare user create --username admin --role admin

# メールアドレスを登録する（パスワードは対話入力できる）
mailcare mailbox add --address bounce@example.com --host imap.example.com --username bounce@example.com

# 今すぐメールをチェックする（サーバー稼働中はサーバーにジョブを投入する。--wait で完了まで進捗を表示する）
mailcare check --wait

# チェック時刻を変更する（起動時の --check-time 06:00 --check-time 12:00 ... でも同じ。指定した値は保存され次回から省略できる）
mailcare schedule set 06:00 12:00 18:00
```

Web 画面のナビゲーション:

| メニュー | 内容 |
| --- | --- |
| ダッシュボード（ブランドをクリック） | メールアドレスごとの未対応グループ数・最終チェック、直近の纏めグループ、実行中ジョブ、次回チェック時刻 |
| アラート | メールアドレス → 纏めグループ（分析報告付き） → 元メール一覧 → メール詳細 |
| メール | メールアドレスごとの生メール閲覧（テキスト / HTML / 原文ダウンロード） |
| ツール | 索引の再作成、判定の再実行、エージェント分析の再実行、ジョブ履歴 |
| 設定 | チェック時刻とエージェント、メールアドレス、利用者、ログイントークン、アカウント |

詳しい設計は [Documents/システム設計書.md](Documents/システム設計書.md)、テーブルは [Documents/テーブル定義.md](Documents/テーブル定義.md)、
エージェントへのプロンプトは [Documents/プロンプト仕様](Documents/プロンプト仕様) を参照してください。

### 1.2 動作要件

- エージェント分析を使う場合は `codex` CLI が PATH にあること（無い場合は分析だけがスキップされ、他の機能は動作します）。
- 実行時データは実行ファイルと同じ場所の `data/` に作成されます（`--data-dir` で変更できます）。
  `data/mailcare.key` は IMAP パスワードの暗号化と Web セッションの署名に使う秘密鍵です。バックアップと権限（0600）に注意してください。

## 2. 開発者向けリファレンス

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
go mod tidy --go=1.25
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
実行時データ（DB ファイル、メール原文、エージェント作業領域）は実行ファイルと同じ場所の `data/` に作成されます。

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
