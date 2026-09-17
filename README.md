# MailCare

Japanese version: [README-ja.md](README-ja.md)

## 1. Overview

MailCare watches any number of mail addresses over IMAP, collects the notices that mail daemons
(MAILER-DAEMON, postmaster, ...) send back, such as "Undelivered Mail Returned to Sender", smooths out the
variation in recipient addresses and remote server IPs, and presents them in **bounce groups**: one row per
problem that the mail server administrator, the domain administrator or the owner of the recipient address
has to act on.

- **Every mail is kept** - each received mail, whether it is a bounce or not, is stored under
  `data/mails/<address>/` as the original (`.eml`), the text body (`.txt`), the HTML body (`.html`) and the
  parsed metadata (`.json`). The index is a separate SQLite file per address (`data/mails/<address>.sqlite`),
  so detection can be re-run from the raw files whenever the rules change.
- **Detection** - the sender, sender name, subject and `multipart/report` delivery status decide whether a
  mail is a bounce and of which kind (failed / delayed / auto-reply). The failing recipient, the extended
  status code (`5.1.1`), the remote MTA and IP and the diagnostic text are extracted.
- **Grouping** - bounces with the same recipient domain, status code and normalized diagnostic text form one
  group, with a machine-derived guess of who should act (sending server, recipient address owner, recipient
  domain).
- **Agent analysis** - an agent CLI installed on the machine (Codex CLI today; more can be added) is given the
  paths of the raw mails of a group and writes a cause analysis with recommended actions, stored per group.
- **Scheduled checks** - mail is fetched at the configured daily times (default 06:00, 12:00 and 18:00). The
  first check of an address looks back 90 days, later checks 30 days, and only mails not fetched yet are
  taken.
- **Web and CLI** - everything can be done from the Web UI (port 9790). The command line covers starting and
  stopping the service, user and login-token management, mail address registration, mail checks, reindexing,
  analysis runs and group listings (reading mail bodies is Web only).
- One binary for Windows, macOS and Linux.

### 1.1 Quick start

```bash
# Start the server (open http://localhost:9790/ and create the first administrator on the setup screen)
mailcare service start

# Or create an administrator from the command line
mailcare user create --username admin --role admin

# Register a mail address (the password is prompted when omitted)
mailcare mailbox add --address bounce@example.com --host imap.example.com --username bounce@example.com

# Check mail now (submitted to the running server; --wait shows the progress until it finishes)
mailcare check --wait

# Change the check times (the same as --check-time 06:00 --check-time 12:00 ... at start; saved for later runs)
mailcare schedule set 06:00 12:00 18:00
```

Web navigation:

| Menu | Content |
| --- | --- |
| Dashboard (click the brand) | Open groups and last check per address, recent groups, running jobs, next check time |
| Alerts | Mail address -> bounce group (with the agent report) -> original mails -> mail detail |
| Mail | Raw mail viewer per address (text / HTML / original download) |
| Tools | Rebuild the index, re-run detection, re-run the agent analysis, job history |
| Settings | Check times and agent, mail addresses, users, login tokens, account |

See [Documents/システム設計書.md](Documents/システム設計書.md) for the design,
[Documents/テーブル定義.md](Documents/テーブル定義.md) for the tables and
[Documents/プロンプト仕様](Documents/プロンプト仕様) for the agent prompt (all in Japanese).

### 1.2 Requirements

- The `codex` CLI must be on PATH to use the agent analysis (without it only the analysis is skipped).
- Runtime data is created in `data/` next to the executable (`--data-dir` changes it). `data/mailcare.key` is
  the secret key that encrypts IMAP passwords and signs Web sessions: back it up and keep it at mode 0600.

## 2. Developer reference

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
go mod tidy --go=1.25
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
same port). Runtime data (database, raw mails, agent workspaces) is created in `data/` next to the executable.

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
