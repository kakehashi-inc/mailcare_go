# MailCare

Japanese version: [README-ja.md](README-ja.md)

## 1. Overview

MailCare groups the bounces ("could not be delivered" and "delivery delayed" notices) that arrive at the mailboxes it
monitors by cause, and shows the problems the sending side has to act on. It also sends probe mails and checks that
they arrive, so that delays and failures in sending and receiving mail are noticed.
It is a single executable for Windows, macOS and Linux, operated from a web browser.

### 1.1 Features

- **Bounce monitoring and grouping** — fetches several mailboxes over IMAP at set times and groups the bounces by
  cause, such as "sending IP blocked", "sender authentication failed" or "recipient address does not exist".
  Failed records of DMARC aggregate reports are grouped too. Bounces that report a failed name lookup are checked by
  looking the name up again, to find the cause (DNS check).
- **Alerts** — only what the sending side has to act on becomes an alert; problems on the receiving side are listed
  separately as "Excluded".
  Each alert is open, resolved or ignored; a resolved or ignored alert that receives the same problem again comes
  back for re-check.
  The overview of an alert can be downloaded as a PDF report, and as a ZIP together with its source mails.
- **Cause analysis by an AI agent** — an AI agent (Codex CLI) writes a cause analysis and the steps to take for each
  alert, automatically for new alerts and new kinds of bounces.
- **Notification emails** — the list of alerts to act on is mailed to the users chosen as recipients.
- **Delivery probe (send check)** — at set times, each mailbox sends a probe mail through its SMTP server, and MailCare
  checks that it arrives in a probe mailbox and reports delays and failures on the screen and by mail.

Also available:

- Junk and fake delivery failure notices are recognized and kept out of the alerts.
- Fetched mail can be browsed on the screen.
- Mail past the retention is deleted every day. Notices and junk can also be deleted from the IMAP server (this
  cannot be undone; for a mailbox that receives mail that must be kept, set it not to delete).
- Administrators and members; the screens are in Japanese and English, with light and dark themes.
- The main operations are also available from the command line.

### 1.2 Requirements

- **OS**: Windows, macOS or Linux (64-bit, amd64 or arm64).
- **Port**: the web screens use port 9790 (configurable).
- **Monitored mail accounts**: reachable over IMAP. Deleting mail from the IMAP server needs the right to delete.
- **For cause analysis by an AI agent**: install [Codex CLI](https://github.com/openai/codex) and sign in with the OS
  user that runs MailCare (`codex login`).
- **For notification emails**: an SMTP server to send with.
- **For the delivery probe**: a dedicated mail account that receives the probe mails (IMAP, with the right to delete)
  and the SMTP server of each mailbox.
- **DNS check** (on by default): outbound DNS (port 53). Where it is not available, turn the check off or change the
  resolvers in the settings.

### 1.3 Installation and first setup

1. Extract the distributed ZIP (`mailcare_<version>_<os>_<cpu>.zip`) and put the executable `mailcare`
   (`mailcare.exe` on Windows) in its installation directory.
2. Start the server.

   ```bash
   mailcare service start
   ```

   Main start options (the values given are saved and can be omitted next time):

   | Option | Meaning |
   | --- | --- |
   | `--web-listen` / `--web-port` | Listen address and port (default `0.0.0.0` / `9790`) |
   | `--data-dir` | Data directory (default: `data/` next to the executable) |
   | `--check-time` | Sync times (repeat it: `--check-time 06:00 --check-time 12:00`) |
   | `--workers` | Concurrent jobs (1-16, default 2) |
   | `--mail-keep-days` | Mail retention in days (default 180) |

3. Open `http://<server>:9790/` in a browser and create the first administrator on the setup screen.
4. Register the mailboxes to monitor in "Settings -> Mailboxes" and check the IMAP connection with
   "Test connection". Enter the SMTP server used for sending in "SMTP server" too (it serves the sending source
   check and the delivery probe).
5. Configure the rest as needed:
   - "Settings -> General": sync times, cleanup, sending sources and DNS check, AI agent
   - "Settings -> Notifications": SMTP server, recipients, notification time
   - "Settings -> Delivery probe": probe address, probe server and send times
   - "Settings -> Users": users and roles

`mailcare service start` runs in the foreground. To keep it running, register it as an OS service. Example for Linux
(systemd):

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

Stop it with `mailcare service stop` and check it with `mailcare service status`. Run the commands as the OS user of
the service with the same `--data-dir` (for example `sudo -u mailcare /opt/mailcare/mailcare service stop`).

### 1.4 Data and backup

- All data is stored in `data/` (changeable with `--data-dir`), including the key that encrypts the passwords.
  **Back up the whole `data/` directory.**
- To upgrade, stop the server, replace the executable and start it again. The data is updated automatically on start.
- After an upgrade that changes the classification, or after changing the sending sources or the DNS check, run
  "Reclassify" once on the Tools page.
  Bounces that become junk can then be deleted from the IMAP server by the next cleanup; check the Junk tab first.

### 1.5 Security

- Run MailCare and Codex CLI as a dedicated, unprivileged OS user that can read nothing but `data/`.
- Keep the `data/` directory unreadable for other users.
- MailCare serves plain HTTP. Over a network, put a reverse proxy with HTTPS in front of it and let MailCare accept
  only the proxy with `--web-listen 127.0.0.1`. Do not pass `/control/` through the proxy.

### 1.6 Outbound connections

Besides the configured mail servers (IMAP and SMTP), MailCare connects to:

- **DNS check** (on by default) — names that failed in bounces and related names are looked up through the configured
  resolvers (Cloudflare, Google and Quad9 by default). Only for notices handed in by a sending source are the name
  servers of the domain asked directly.
  They learn the IP address of this server and the names asked. To avoid this, turn the DNS check off or turn on
  "Use only the OS resolver".
- **AI agent** — for the analysis, the overview of the alert and sample bounces are sent to OpenAI through Codex CLI.
  To send nothing, do not sign in to Codex CLI with the OS user that runs MailCare.

### 1.7 Command line

When the server is running, command-line operations are handed to it. Main commands:

| Command | Meaning |
| --- | --- |
| `mailcare user create --username admin --role admin` | Create a user |
| `mailcare mailbox add --address a@example.com --host imap.example.com --username a@example.com` | Register a mailbox to monitor (asks for the password) |
| `mailcare sync --wait` | Sync all mailboxes now |
| `mailcare schedule set 06:00 12:00 18:00` | Change the sync times |
| `mailcare settings show` / `mailcare settings set <key> <value>` | Show or change the settings |
| `mailcare reclassify` | Classify the fetched mail again (Reclassify) |
| `mailcare cleanup` | Run the cleanup now |
| `mailcare probe send` / `mailcare probe check` | Send the probe mails now / check their arrival now |
| `mailcare jobs list` | Show the job history |

All commands are listed by `mailcare --help`.

### 1.8 Bundled fonts

The PDF reports use BIZ UDPGothic (Copyright 2022 The BIZ UDGothic Project Authors,
[SIL Open Font License 1.1](embedded/fonts/OFL.txt)) and Go Mono (BSD license), both built
into the executable.

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

The Web UI is served at <http://localhost:9790> (`service stop` / `status` connect to the control endpoints on the
same port). Runtime data (the master database, the per-mailbox indexes with the original messages and body
sections, the per-run agent working directories) is created in `data/` next to the executable.

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
