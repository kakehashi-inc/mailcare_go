# MailCare

Japanese version: [README-ja.md](README-ja.md)

## 1. Overview

MailCare collects the "could not be delivered" and "delivery delayed" notices (bounces) that arrive at the mail
addresses it watches, groups them by cause and tells you which ones the sending side has to act on.
It is a single executable for Windows, macOS and Linux, operated from a web browser.

### 1.1 Features

- **Mail address monitoring** - watches any number of mail addresses over IMAP and fetches new mail at fixed
  times of the day (06:00, 12:00 and 18:00 by default).
- **Bounce detection and grouping** - recognizes bounces among the fetched mail and groups them by cause, such
  as "sending IP blocked", "sender authentication failed" or "recipient address does not exist". Bounces that
  call for the same action end up in one group even when the recipients or remote servers differ.
- **Alerts** - only what the sending side has to act on is shown as an alert; recipient-side problems (unknown
  user, full mailbox, ...) are listed separately as excluded. Each alert can be marked open, resolved or
  ignored.
- **AI cause analysis** - an AI agent (Codex CLI) writes a cause analysis and recommended actions for each
  alert, automatically for new alerts and when a new kind of notice arrives for an existing one. Reports whose
  cause could not be established are marked "Needs review".
- **Mail notifications** - sends the list of alerts that need attention to the selected users, each in their
  own language (Japanese / English) and time zone. The time and the interval (daily to every 7 days) are
  configurable.
- **Mail viewer** - browse the fetched mail by all / bounces / others (text and HTML bodies, original download).
- **Automatic cleanup** - fetched mail is removed after its retention (180 days by default). The mails of
  resolved or ignored alerts can also be deleted from the IMAP server after a number of days set per mail
  address (60 by default for a new address; 0 never deletes).
- **Users and roles** - administrators and read-only users. The UI is available in Japanese and English, with
  light and dark themes.
- **Command line** - starting and stopping, registering users and mail addresses, syncing and more can also be
  done from the command line.

### 1.2 Requirements

- **OS**: Windows / macOS / Linux (64-bit, amd64 or arm64).
- **Port**: the web UI listens on port 9790 (configurable). Make it reachable from the users' browsers (or
  through a reverse proxy).
- **Monitored mail accounts**: reachable over IMAP. Deleting mail on the IMAP server requires the right to
  delete mail.
- **AI cause analysis** (optional): install [Codex CLI](https://github.com/openai/codex) and sign in with the OS
  user that runs MailCare (`codex login`). Everything except the analysis works without it.
- **Mail notifications** (optional): an SMTP server to send through.

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
   | `--check-time` | Daily check times (repeat it: `--check-time 06:00 --check-time 12:00`) |
   | `--workers` | Tasks run at once (1-16, default 2) |
   | `--mail-keep-days` | Retention of fetched mail in days (default 180) |

3. Open `http://<server>:9790/` in a browser and create the first administrator on the setup screen.
4. Register the mail addresses to watch in "Settings -> Mailboxes" and check the IMAP connection with
   "Test connection".
5. Configure the rest as needed:
   - "Settings -> General": check times, mail retention, AI agent (model, reasoning level, automatic analysis
     on / off)
   - "Settings -> Notifications": SMTP server, recipient users, notification time and interval
   - "Settings -> Users": users and their roles

`mailcare service start` runs in the foreground. To keep it running, register it as an OS service. Linux
(systemd) example:

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

Stop it with `mailcare service stop` and check it with `mailcare service status`.

### 1.4 Data and backup

- All data is stored in `data/` (`--data-dir` changes it): the fetched mail, the alerts and their analyses,
  the settings and the users.
- The key that encrypts the IMAP / SMTP passwords is also inside `data/mailcare.db`. **Back up the whole
  `data/` directory**; there is no separate key to keep.
- To upgrade, stop the server, replace the executable and start it again. The data is updated for the new
  version automatically at start.

### 1.5 Security

- Run MailCare and Codex CLI as a dedicated, unprivileged OS user that can read nothing but `data/`. The mails
  being analyzed are untrusted input from outside, and Codex CLI can read whatever that OS user can read.
- Keep the `data/` directory private (mode 0700; the server warns at start otherwise).
- MailCare itself speaks plain HTTP. When it is used over the network, put a reverse proxy in front of it for
  HTTPS, let MailCare accept only the proxy (`--web-listen 127.0.0.1`) and make the proxy pass the `Host`
  header through unchanged.

### 1.6 Command line

While the server is running, command line operations are handed to it. Main commands:

| Command | Meaning |
| --- | --- |
| `mailcare user create --username admin --role admin` | Create a user |
| `mailcare mailbox add --address a@example.com --host imap.example.com --username a@example.com` | Register a mail address to watch (the password is prompted for) |
| `mailcare sync --wait` | Sync every mail address now (fetch -> group -> analyze) |
| `mailcare schedule set 06:00 12:00 18:00` | Change the automatic check times |
| `mailcare settings show` / `mailcare settings set <key> <value>` | Show / change the settings |
| `mailcare cleanup` | Apply the retentions now |
| `mailcare jobs list` | Show the task history |

`mailcare --help` lists every command.

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
