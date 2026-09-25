# MailCare

Japanese version: [README-ja.md](README-ja.md)

## 1. Overview

MailCare watches any number of mail addresses over IMAP, collects the notices that mail daemons
(MAILER-DAEMON, postmaster, ...) send back, such as "Undelivered Mail Returned to Sender", smooths out the
variation in recipient addresses and remote server IPs, and presents them in **bounce groups**: one row per
problem that the mail server administrator, the domain administrator or the owner of the recipient address
has to act on.

- **Every mail is kept** - each received mail, whether it is a bounce or not, is stored under
  `data/mails/<address>/<year>/<month>/` (split by the date of the mail) as the original (`.eml`) and its decoded body sections (text as `<key>-1.txt`,
  `<key>-2.txt`, ..., HTML as `<key>-1.html`, `<key>-2.html`, ...: one file per part with content, in MIME
  order). The headers and other metadata live only in the index, a separate SQLite file per address
  (`data/mails/<address>.sqlite`), so detection can be re-run from the raw files whenever the rules change.
  Mails are kept for a configurable retention (180 days by default, counted from the date of the mail): a
  cleanup that runs once a day, when the date changes, removes older mails, files and index entry alike, and a
  bounce group left without mails disappears together with its analysis. The same cleanup drops finished jobs
  older than 30 days from the job history. It can also be started from the Tools screen or the CLI.
- **Detection** - the sender, sender name, subject and `multipart/report` delivery status decide whether a
  mail is a bounce and of which kind (failed / delayed / auto-reply). The failing recipient, the extended
  status code (`5.1.1`), the remote MTA and IP and the diagnostic text are extracted.
- **Grouping** - each bounce is assigned a category (sending IP blocked, sender address rejected, sending
  domain authentication failure, user unknown, mailbox full, ...) and grouped by the unit the administrator
  acts on (the sending server IP, the sender address, the sending domain) and the party deciding the outcome
  (a blacklist provider, the recipient domain). Recipient-side problems (user unknown, mailbox full, ...) are
  recorded but excluded from Alerts, which show only what the mail administrator has to act on.
- **Agent analysis** - an agent CLI installed on the machine (Codex CLI today; more can be added) is given the
  paths of the raw mails of an actionable group and writes a cause analysis with recommended actions, stored
  per group. A group that gains new mails is analyzed again. Every run gets its own workspace directory
  (prompt, CLI output, report) under `data/agent/<address>/<group_key>/<report_id>/`; directories older than
  the configured retention (30 days by default) are removed by the same daily cleanup.
- **Independent phases** - fetching, grouping and analysis are separate jobs that can be run one by one from
  the Tools screen or the CLI, or together as a sync of all addresses. The number of concurrent jobs
  (workers) is configurable; accounts on the same IMAP server are processed one after another automatically.
- **Scheduled checks** - all addresses are synced at the configured daily times (default 06:00, 12:00 and
  18:00). The first fetch of an address looks back 90 days, later fetches 30 days (never further back than the
  mail retention), and only mails not fetched yet are taken.
- **Mail notifications** - with SMTP configured, the selected recipient users (each user may register an
  optional email address) get one mail listing every analyzed actionable group: its summary, the number of
  affected mails and the URL that opens the alert. The notification time and interval (daily to every 7 days)
  are configurable. The stored SMTP password is used only for the server it was saved for: changing the host,
  port, connection mode or username (in the settings screen, with `settings set`, or for a test mail) requires
  entering the password again.
- **Web and CLI** - everything can be done from the Web UI (port 9790). The command line covers starting and
  stopping the service, user and token management, mail address registration, sync / fetch / group /
  analyze / cleanup runs, reindexing and group listings (reading mail bodies is Web only). Tokens are reserved for a
  future API.
- One binary for Windows, macOS and Linux.

### 1.1 Quick start

```bash
# Start the server (open http://localhost:9790/ and create the first administrator on the setup screen)
mailcare service start

# Or create an administrator from the command line
mailcare user create --username admin --role admin

# Register a mail address (the password is prompted when omitted)
mailcare mailbox add --address bounce@example.com --host imap.example.com --username bounce@example.com

# Sync now (fetch, group, analyze; submitted to the running server; --wait shows the progress)
mailcare sync --wait

# Run only one phase
mailcare fetch
mailcare group
mailcare analyze

# Change the check times (the same as --check-time 06:00 --check-time 12:00 ... at start; saved for later runs)
mailcare schedule set 06:00 12:00 18:00
```

Web navigation:

| Menu | Content |
| --- | --- |
| Dashboard (click the brand) | Actionable open groups and last fetch per address, recent groups, running jobs, next check time (non-admin users are read-only) |
| Alerts | Mail address -> bounce group (actionable / excluded switch, with the agent report) -> original mails -> mail detail |
| Mail | Raw mail viewer per address (text / HTML / original download) |
| Tools | Sync all addresses, fetch only, group only, analyze only, rebuild the index, re-run detection, cleanup (apply the retentions now), job history |
| Settings (administrators only) | Check times, mail retention, workers and agent, notifications (SMTP, recipients, time, interval), mail addresses, users, tokens (reserved for the API) |
| User name (top right) | Profile (display name, notification address, language, time zone, theme, password) and logout |

See [Documents/システム設計書.md](Documents/システム設計書.md) for the design,
[Documents/テーブル定義.md](Documents/テーブル定義.md) for the tables and
[Documents/プロンプト仕様](Documents/プロンプト仕様) for the agent prompt (all in Japanese).

### 1.2 Requirements

- The `codex` CLI must be on PATH to use the agent analysis (without it only the analysis is skipped).
- Runtime data is created in `data/` next to the executable (`--data-dir` changes it). It holds exactly three
  things:
  - `data/mailcare.db` - the master database (users, tokens, settings, mail addresses, jobs). The secret key
    that encrypts the IMAP / SMTP passwords and signs the Web sessions is stored inside it (`settings` table);
    there is no key file, so a backup of this one file also preserves the key.
  - `data/mails/` - the index per address (`<address>.sqlite`) and the raw mail directories (`.eml` plus the
    `.txt` / `.html` body sections).
  - `data/agent/` - the per-run agent workspaces (`<address>/<group_key>/<report_id>/`).
- Run MailCare and the agent CLI as a dedicated, unprivileged OS user that can read nothing but `data/`. The
  agent's read-only sandbox only prevents writes: the CLI can still read every file the OS user can read (home
  directory, keys, other applications' settings), and the mails it analyzes are untrusted input. Keep the data
  directory private (mode 0700; the server warns at start otherwise). When MailCare is reached over the
  network, terminate TLS on a reverse proxy in front of it (the server itself speaks plain HTTP), let the proxy
  add the `Secure` flag to the session cookie or bind `web_listen` to `127.0.0.1`, and make the proxy pass the
  `Host` header through unchanged.

## 2. Developer reference

### Finding the IP address inside WSL for debugging

```bash
hostname -I | awk '{print $1}'
```

### Generating the icons

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

### Go commands

Install or update the debugger

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
```

Add a module

```bash
go get <package-name>
```

Add and build a module

```bash
go install <package-name>
```

Create the module file

```bash
go mod init <module-name>
```

Download modules (all of go.mod when the name is omitted)

```bash
go mod download <module-name>
```

Tidy modules (sources and go.mod in both directions)

```bash
go mod tidy
```

Update modules

```bash
go get -u
```

Update the Go version

```bash
go mod tidy --go=1.26
```

Clear the caches

```bash
go clean --cache --testcache
```

### Frontend commands

Install dependencies

```bash
cd frontend
yarn install
```

Build (outputs `frontend/dist`; required before `go build` and `go vet` because Go embeds it)

```bash
cd frontend
yarn build
```

Type check

```bash
cd frontend
yarn lint
```

Format (`src`)

```bash
cd frontend
yarn format
```

Copy the Material Icons fonts (from `node_modules` to `public/fonts`)

```bash
cd frontend
yarn setup:fonts
```

### Run

```bash
go run . service start
```

The Web UI is served at http://localhost:9790 (`service stop` / `status` connect to the control endpoints on the
same port). Runtime data (the master database, the per-address indexes with the raw mails and body sections, the
per-run agent workspaces) is created in `data/` next to the executable.

### Lint and tests

```bash
make lint
make test
```

### Build and release

Build (run `cd frontend && yarn build` first)

```bash
go build
```

Release

```bash
make
```
