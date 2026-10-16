# MailCare

Japanese version: [README-ja.md](README-ja.md)

## 1. Overview

MailCare collects the "could not be delivered" and "delivery delayed" notices (bounces) that arrive at the
mailboxes it monitors, groups them by cause and shows which ones the sending side has to act on.
It is a single executable for Windows, macOS and Linux, operated from a web browser.

### 1.1 Features

- **Mailbox monitoring** - monitors any number of mailboxes over IMAP and fetches new messages at fixed times of
  the day (the sync times; 06:00, 12:00 and 18:00 by default).
- **Bounce detection and grouping** - recognizes bounces among the fetched messages and groups them by cause,
  such as "Sending IP blocked", "Sender authentication failed" or "Recipient does not exist". Bounces that call
  for the same action end up in one group even when the recipients or remote servers differ. DMARC aggregate
  reports are read as well: every record that failed DMARC is grouped by what it shows ("SPF record missing",
  "DKIM verification failed", "Sender not authenticated") together with its sending IPs. Bounces that report a
  failed name lookup ("Host not found, try again" and the like) are checked by looking the name up again (the DNS
  check), which tells apart a recipient domain that does not exist, a DNS problem on the receiving side and a name
  resolution problem of the sending side's mail server.
- **Alerts** - only what the sending side has to act on is shown as an alert; recipient-side problems
  (nonexistent addresses, full mailboxes and so on) are listed separately as excluded. Each alert can be marked
  open, resolved or ignored. A resolved or ignored alert that receives a bounce showing the decision did not
  settle the problem comes back as "Resolved (re-check)" or "Ignored (re-check)".
  The overview and statistics of an alert can be downloaded as a PDF report, and as a ZIP together with its
  source messages (original message, headers, bodies and their index records) and a README.md describing the
  format.
- **AI cause analysis** - an AI agent (Codex CLI) writes an analysis report (cause and recommended actions) for
  each alert, automatically for new alerts and when a bounce of a new pattern arrives for an existing one
  (automatic analysis). Reports whose cause could not be established are marked "Needs review". MailCare hands
  Codex CLI a summary of the alert (including the recipient addresses), sample bounces (their headers, the
  headers of the returned message and excerpts of their bodies; the full bodies are available to it as files)
  and the results of the DNS check, and Codex CLI sends them to OpenAI (see 1.6).
- **Notification emails** - sends the list of alerts that need action to the selected users, in each user's
  language (Japanese / English) and time zone. The notification time and the interval (every day to every 7
  days) are configurable.
- **Junk detection** - typical phishing (a sender name that impersonates the monitored domain or another
  organization, a link to an outside site that carries the monitored address, a forged From of the monitored
  domain, a fake "delivery failed" notice that carries links to other sites or fails the sender check) and mail
  flagged as spam are recognized as junk, and so is a bounce of mail that was not sent from the sending sources
  (the sending servers registered in the settings, see 1.3): a third party used the monitored address as the
  sender. Junk is not an alert and is counted separately.
- **Message viewer** - browse the fetched messages by All / Targets / Junk / Other (text and HTML bodies,
  download of the original message).
- **Cleanup** - every day at the cleanup time (2:00 by default), messages past the mail retention (180 days by
  default) are deleted. Bounces, auto-replies, DMARC reports and similar notices, and junk mail, can also be
  deleted from the IMAP server after a number of days set per mailbox (the server retention; 60 by default for
  a new mailbox, at most the mail retention; 0 never deletes). Notices recognized by subject or sender name
  alone, automatically generated messages and ordinary mail stay on the server. Deletion on the IMAP server
  cannot be undone, and junk detection can be wrong: for a mailbox that receives mail that must be kept by law or
  policy (such as records of electronic transactions), set its server retention to 0 or archive the mail first.
- **Users and roles** - administrators and members. Members can view the dashboard, the alerts and the messages
  and change the state of alerts. The settings, the tools, the jobs and the connection details (the IMAP
  connection and the "SMTP server (sending)" of the mailboxes, the sending sources, the DNS resolvers) are for
  administrators only. The screens are available in Japanese and English, with light and dark themes.
- **Command line** - starting and stopping, registering users and mailboxes, syncing and more can also be done
  from the command line.

### 1.2 Requirements

- **OS**: Windows / macOS / Linux (64-bit, amd64 or arm64).
- **Port**: the web UI listens on port 9790 (configurable). Make it reachable from the users' browsers (or
  through a reverse proxy).
- **Monitored mail accounts**: reachable over IMAP. Deleting mail on the IMAP server requires the right to
  delete mail.
- **AI cause analysis** (optional): install [Codex CLI](https://github.com/openai/codex) and sign in with the OS
  user that runs MailCare (`codex login`). Everything except the analysis works without it.
- **Notification emails** (optional): an SMTP server to send through.
- **DNS check** (on by default, optional): when a bounce reports a failed name lookup, MailCare looks the name up
  again. This needs outbound DNS (UDP and TCP port 53) to the resolvers set in General settings (Cloudflare
  1.1.1.1, Google 8.8.8.8 and Quad9 9.9.9.9 by default). The name servers of the domain concerned are asked
  directly only for notices handed in by a sending source (as recorded by the receiving servers of the monitored
  mailbox), and never while "Use only the OS resolver" is on. While the check is on, the SPF records of the
  domains of the monitored mailboxes are read as well. The host names of the sending sources are always looked
  up, through the operating system's resolver first. With the
  check off, MailCare sends no DNS query except the lookups of these host names through the operating system's
  resolver, and the results stored earlier still classify the bounces. If the server cannot reach the resolvers,
  turn the DNS check off, change the resolvers or turn on "Use only the OS resolver" under "Sending sources and
  DNS check" in "Settings -> General". See 1.6 for what these queries reveal.

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
   "Test connection".
5. Configure the rest as needed:
   - "Settings -> Mailboxes": for each mailbox, enter in "SMTP server (sending)" the host name of the SMTP server
     used to send its mail (for example `mail.example.jp`). Without a host name for a mailbox (here or in
     "Sending source host names" of General settings), MailCare cannot identify, for that mailbox, the bounces of
     mail that was not sent from the sending sources.
   - "Settings -> General": sync times, cleanup (cleanup time, mail retention), days before re-check, sending
     sources and DNS check (the sending sources that belong to no mailbox go into "Sending source IPv4", "Sending
     source IPv6" and "Sending source host names"; a host name here counts for every mailbox), concurrent jobs and
     the AI agent (provider, model, reasoning level, automatic analysis on / off)
   - "Settings -> Notifications": SMTP server, recipients, notification time and interval
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

- All data is stored in `data/` (`--data-dir` changes it): the fetched messages, the alerts and their analysis
  reports, the settings and the users, the results of the DNS check (kept for the mail retention plus one day)
  and the looked-up addresses of the sending sources (host name addresses and SPF; kept for up to 90 days).
- The key that encrypts the IMAP / SMTP passwords is also inside `data/mailcare.db`. **Back up the whole
  `data/` directory**; there is no separate key to keep.
- To upgrade, stop the server, replace the executable and start it again. The data is updated for the new
  version automatically at start.
- Messages that are already fetched are not classified again by new rules automatically. After an upgrade that
  changes how bounces are recognized, and after changing the sending sources (in "Settings -> General" or the
  "SMTP server (sending)" of a mailbox) or the DNS check settings, run "Reclassify" once (Tools page, or
  `mailcare reclassify`).
  - While the DNS check is on, Reclassify also looks up the names reported by stored bounces of the last two days
    (older bounces use only the DNS check results stored before).
  - It can turn older bounces into junk, and junk older than the server retention of a mailbox is deleted from the
    IMAP server at the next cleanup. Review the Junk tab of the message list first, or set the server retention of
    the mailbox to 0 until the review is done.
  - Alerts that end up without any messages are removed together with their state and history.

### 1.5 Security

- Run MailCare and Codex CLI as a dedicated, unprivileged OS user that can read nothing but `data/`. The
  messages being analyzed are untrusted input from outside, and Codex CLI can read whatever that OS user can read.
- Keep the `data/` directory private (mode 0700; the server warns at start otherwise).
- MailCare itself speaks plain HTTP. When it is used over the network, put a reverse proxy in front of it for
  HTTPS, let MailCare accept only the proxy (`--web-listen 127.0.0.1`) and make the proxy pass the `Host`
  header through unchanged.

### 1.6 Outbound connections

Besides the IMAP servers of the monitored mailboxes and the SMTP server of the notification emails, MailCare
connects to the following.

- **DNS check (on by default).** MailCare sends plain, unencrypted DNS queries (UDP, and TCP for long answers;
  port 53) to the resolvers in "Settings -> General" (Cloudflare 1.1.1.1, Google 8.8.8.8 and Quad9 9.9.9.9 by
  default), or only to the operating system's resolver while "Use only the OS resolver" is on. It asks for:
  - a fixed control name (`example.com`), at most once per resolver and run, when the run first uses that
    resolver, to check that it answers;
  - the names whose lookup failed according to bounces of the last two days (only names in the recipient's
    domain or among its mail servers), and the mail servers (MX) of those recipient domains;
  - the host name of the server that issued such a bounce, to tell whether it is the sending side's mail server;
  - the SPF records of the domains of the monitored mailboxes and the names they refer to;
  - the addresses of the host names of the sending sources (the operating system's resolver first).

  Every query to the configured resolvers (the control query included) asks them not to pass this server's
  network (subnet) on to other name servers (EDNS Client Subnet with a prefix length of 0).

  The name servers of the domain concerned are asked directly only for a notice handed in by a sending source:
  this is decided by the connecting IP address that the receiving servers of the monitored mailbox recorded when
  the notice arrived, never by the content of the notice (host names, Received lines, DKIM signatures). While
  "Use only the OS resolver" is on, nothing is asked directly.
- **What the other side learns.** The resolvers, the name servers asked directly and the networks in between see
  this server's IP address, the time and the names asked. The names come from the bounces, so a third party who
  can send a fake bounce to a monitored mailbox can make this server look up names of that third party's
  choosing. Copying host names, Received lines or DKIM signatures into the notice does not make this server ask
  name servers directly. A third party's notice can still lead to direct queries when the SPF records of the
  domains of the monitored mailboxes include ranges shared with other senders (large mail services, for example), when a proxy or
  NAT in front of the receiving servers records the connecting address as a private IP address, or when a server
  registered as a sending source is also the receiving server of the monitored mailbox and relays over a global
  IP address. If this server's IP address must not be disclosed that way, turn the DNS check off.
- **With the DNS check off** ("Settings -> General", or `mailcare settings set dns_check_enabled 0`), the only DNS
  queries are the lookups of the host names of the sending sources through the operating system's resolver.
  The results stored earlier still classify the bounces. "Use only the OS resolver" keeps the check on but
  sends every query to the operating system's resolver and none to the name servers directly.
- **Stored results.** The results of the DNS check, with the answers of each resolver and name server, are stored
  in `data/` for the mail retention plus one day; the looked-up addresses of the sending sources for up to 90
  days.
- **AI cause analysis.** When Codex CLI is installed and signed in, the analysis hands it the alert summary
  (including the recipient addresses), sample bounces (headers, the headers of the returned message, body
  excerpts and full bodies as files) and the DNS check results, and Codex CLI sends them to OpenAI. Turning off
  "Enable automatic analysis" in "Settings -> General" stops the automatic runs; an administrator can still start
  an analysis by hand. To send nothing to OpenAI, do not install or sign in Codex CLI with the OS user that runs MailCare.

### 1.7 Command line

While the server is running, command line operations are handed to it. Main commands:

| Command | Meaning |
| --- | --- |
| `mailcare user create --username admin --role admin` | Create a user |
| `mailcare mailbox add --address a@example.com --host imap.example.com --username a@example.com` | Register a mailbox to monitor (the password is prompted for) |
| `mailcare sync --wait` | Sync every mailbox now (fetch -> group -> analyze). Add `--all-time` to check every message regardless of the period |
| `mailcare schedule set 06:00 12:00 18:00` | Change the sync times |
| `mailcare mailbox update a@example.com --smtp-host mail.example.jp` | Set the "SMTP server (sending)" of a mailbox (`mailbox add` takes `--smtp-host` too; `--smtp-host ""` clears it; no password needed) |
| `mailcare settings show` / `mailcare settings set <key> <value>` | Show / change the settings |
| `mailcare settings set sending_source_hosts mail1.example.jp,mail2.example.jp` | Set the sending sources that belong to no mailbox (`sending_source_ipv4`, `sending_source_ipv6`, `sending_source_hosts`; comma-separated, `""` clears the list) |
| `mailcare settings set dns_check_enabled 0` | Switch the DNS check off (`dns_check_resolvers` sets the resolvers, `dns_check_system_resolver_only 1` uses only the operating system's resolver) |
| `mailcare cleanup` | Run the cleanup now: delete the notices and junk mail past the server retention from the IMAP server, and delete the messages past the mail retention, temporary files, agent working directories and old job history |
| `mailcare jobs list` | Show the job history |

`mailcare --help` lists every command.

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
