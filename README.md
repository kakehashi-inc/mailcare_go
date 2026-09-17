# システム名をここに記載

## 1. システム概要

システムの概略をここに記載してください。

事前の変更点は以下のキーワードでファイル名、またはファイル内容を検索しアプリ名に置き換えてください。

ファイル名キーワード:

- develop_app

ファイル内容検索キーワード:

- develop_app
- develop app
- システム名をここに記載
- 8080（Webサーバーの待受ポート）
- 8081（APIサーバーの待受ポート）
- dap_（APIトークンの接頭辞）
- settings（サーバー設定の保存に暫定利用しているテーブルと処理。不要な場合は削除）

現在の概要に記載されている内容は`システムの概略をここに記載してください。`を残し削除してください。

APIサーバーを使わずWebサーバーのみの1サーバー構成にする場合:

- `app/workers/api_server.go`の`/control/*`（ハンドラとループバック制限）を`web_server.go`の`webHandler`へ移し、`api_server.go`を削除する
- `app/workers/server.go`の`startServer`からAPI側（`apiSrv` / `apiLns`、`apiPort == webPort`の検査）を外し、Web側のリスナーを`listenWithLoopback`で開く
- `api_listen` / `api_port`の定数・設定キー（`app/modules/constants.go`）、`--api-listen` / `--api-port`フラグ（`app/modules/cli.go`）、`StartServer`の引数（`service.go` / `main.go`）を削除し、`service stop` / `status`はWebポートへ接続する

詳細設計書に含めるもの:

- テーブル定義: Documents/テーブル定義.mdに作成してそれを指示してもよい。
- 用語定義: 関係する人の役割、各種用語などは全て定義しておくこと。これは日本語(English)の形式で必ず英語も用意しておく。

プロンプト依頼例:

```text
以下の詳細設計書を元に実装をしてください。
Go言語用にプロジェクトの基本構造などは既に作成済みです。
起動パラメータ解析処理は`main.go`へ実装してください。

`app/models`ディレクトリ内にデータ構造（モデル）を定義してください。
`app/modules`ディレクトリ内に実際の処理や部品を定義してください。
`app/workers`ディレクトリ内にHTTPサーバーのルーティングとハンドラを定義してください。
Web画面は`frontend/src`内に実装してください。

README.mdを確認し、プロジェクトの開発方法などを読み解いてください。**`開発ルール`セクションを確認し遵守してください。**
すべての実装が完了したらREADME.mdのプロジェクトの概要部分を更新してください。
最終的な成果物として、テーブル定義.md、システム設計書.mdをDocumentsに配置してください。

これ以降が詳細設計書です。
```

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

http://localhost:8080 でWeb画面が開きます（APIサーバーは8081。`service stop` / `status`はAPIポートへ接続します）。実行時データ（DBファイルなど）は実行ファイルと同じ場所の`data/`に作成されます。

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
