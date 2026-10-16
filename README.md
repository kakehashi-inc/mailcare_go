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
  call for the same action end up in one group even when the recipients or remote servers differ. DMARC
  aggregate reports are read as well: every record that failed DMARC is grouped by what it shows (SPF record
  missing, DKIM verification failed, sender not authenticated) together with its sending IPs. Notices that report a
  failed name lookup ("Host not found, try again" and the like) are checked by looking the name up again (the DNS
  check), which tells a recipient domain that does not exist from a DNS problem on the receiving side and from a
  name resolution problem of your own mail server.
- **Alerts** - only what the sending side has to act on is shown as an alert; recipient-side problems (unknown
  user, full mailbox, ...) are listed separately as excluded. Each alert can be marked open, resolved or
  ignored.
  The overview and statistics of an alert can be downloaded as a PDF report, and as a ZIP together with its
  source mails (raw message, headers, bodies and their index records) and a README.md describing the format.
- **AI cause analysis** - an AI agent (Codex CLI) writes a cause analysis and recommended actions for each
  alert, automatically for new alerts and when a new kind of notice arrives for an existing one. Reports whose
  cause could not be established are marked "Needs review". MailCare hands Codex CLI a summary of the alert
  (including the recipient addresses), sample notices (their headers, the headers of the returned mail and
  excerpts of their bodies; the full bodies are available to it as files) and the results of the DNS check, and
  Codex CLI sends them to OpenAI (see 1.6).
- **Mail notifications** - sends the list of alerts that need attention to the selected users, each in their
  own language (Japanese / English) and time zone. The time and the interval (daily to every 7 days) are
  configurable.
- **Delivery probe** - at set times (07:00 / 11:00 / 15:00 / 19:00 by default) every mail address with "Send check" on
  actually sends a probe mail, and MailCare checks that it reached the probe server (IMAP). The arrival of all probe mails
  is checked at once a delay threshold (30 minutes by default) after the latest send: a mail not arrived is
  "Delayed", one not arrived within the failure threshold (120 minutes by default) is a "Failure". Both show on
  the dashboard and the Alerts screen, and failures are mailed to the notification recipients. The subject names
  the date and time of the send and the address, so a missing mail can be traced. A mail that arrives late is
  shown as "Recovered" and keeps that it was delayed or failed; a missing mail can be marked resolved with a
  reason (by any user, like an alert). Probe mails whose arrival is confirmed are deleted from the probe server
  to free space (those that could not be deleted then go at the first arrival check a day later; other mail is not touched).
- **Junk detection** - typical phishing (a sender name that impersonates the monitored domain or another
  organization, a link to an outside site that carries the monitored address, a forged From of the monitored
  domain, a fake "delivery failed" notice that carries links to other sites or fails the sender check) and mail
  flagged as spam are recognized as junk, and so is a delivery-failure notice for mail that was never sent from
  your own servers (someone used your address as the sender). Junk is not an alert and is counted separately.
- **Mail viewer** - browse the fetched mail by all / targets / junk / others (text and HTML bodies, original
  download).
- **Automatic cleanup** - every day at a set time (2:00 by default), fetched mail past its retention (180 days by
  default) is removed. Bounces, auto-replies, DMARC reports and similar notices, and junk, can also be deleted
  from the IMAP server after a number of days set per mail address (60 by default for a new address, at most the
  mail retention; 0 never deletes). Notices recognized by subject or sender name alone, automatically generated
  messages and ordinary mail stay on the server. Deletion on the IMAP server cannot be undone, and junk detection
  can be wrong: for an address that receives mail you must keep by law or policy (such as records of electronic
  transactions), set its retention to 0 or archive the mail first.
- **Users and roles** - administrators and members. Members can view the dashboard, the alerts and the mails and
  change the state of alerts. The settings, the tools, the jobs and the connection details (the IMAP and sending
  server settings of the mail addresses, your own sending servers, the DNS resolvers) are for administrators only.
  The UI is available in Japanese and English, with light and dark themes.
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
- **DNS check** (on by default, optional): when a delivery-failure notice reports a failed name lookup, MailCare
  looks the name up again. This needs outbound DNS (UDP and TCP port 53) to the resolvers set in the general
  settings (Cloudflare 1.1.1.1, Google 8.8.8.8 and Quad9 9.9.9.9 by default). The name servers of the domain
  concerned are asked directly only for notices about your own mail, and never while only the operating system's
  resolver is used. While the check is on, the SPF records of the domains of the monitored addresses are read as
  well. The host names of your own sending servers are always looked up, through the operating system's resolver
  first. With the check off, MailCare sends no DNS query except those lookups of your own sending servers through
  the operating system's resolver, and the results stored earlier still classify the notices. If the server
  cannot reach the resolvers, switch the check off, change the resolvers or use only the operating system's
  resolver in "Settings -> General". See 1.6 for what these queries reveal.

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
   - "Settings -> Mailboxes": for each address, the "SMTP server (sending)" that sends its mail, written as that
     server names itself in the mail it handles (for example `mail.example.jp`; optional). For the delivery probe,
     set the connection security, port, username and password in the same "Sending server" section. MailCare works
     without it, but without a host name for an address (here or in the general settings) the bounces of mail that
     was not sent from the sending servers are not told apart for that address, so some may be judged wrongly, and
     the address sends no probe mail.
   - "Settings -> General": check times, cleanup time, mail retention, AI agent (model, reasoning level, automatic
     analysis on / off), your own sending servers that belong to no mail address (IPv4 / IPv6 addresses and host
     names; a host name here counts for every address) and the DNS check
   - "Settings -> Notifications": SMTP server, recipient users, notification time and interval
   - "Settings -> Delivery probe": the probe address and the probe server (IMAP) that receives it, the send times,
     the delay and failure thresholds. For each mail address that sends probe mails, switch on "Send check"
     under "Sending server" in "Settings -> Mail addresses" and set the host name and port of its SMTP server
     (required), the connection security and, when needed, the username and password ("Send a test mail" checks
     it reaches the probe address). An address reserved for the probe is recommended, not required: the probe mails
     are told apart by their subject and probe header, and other mail is not touched. The account must be allowed
     to delete mail: confirmed probe mails are deleted
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
  the settings and the users, the results of the DNS check (kept for the mail retention plus one day) and the
  looked-up addresses of your own sending servers (kept for up to 90 days).
- The key that encrypts the IMAP / SMTP passwords is also inside `data/mailcare.db`. **Back up the whole
  `data/` directory**; there is no separate key to keep.
- To upgrade, stop the server, replace the executable and start it again. The data is updated for the new
  version automatically at start.
- Mail that is already fetched is not judged again by new rules automatically. After an upgrade that changes how
  notices are recognized, and after changing your sending servers (in "Settings -> General" or the SMTP server of
  an address) or the DNS check settings, run "Reclassify" once (Tools screen, or `mailcare reclassify`).
  - While the DNS check is on, Reclassify also looks up the names reported by stored notices of the last two days
    (older notices use only the DNS check results stored before).
  - It can turn older notices into junk, and junk older than the retention of an address is deleted from the IMAP
    server at the next cleanup. Review the Junk tab of the mail list first, or set the retention of the address
    to 0 until you have.
  - Alerts that end up without any mail are removed together with their state and history.

### 1.5 Security

- Run MailCare and Codex CLI as a dedicated, unprivileged OS user that can read nothing but `data/`. The mails
  being analyzed are untrusted input from outside, and Codex CLI can read whatever that OS user can read.
- Keep the `data/` directory private (mode 0700; the server warns at start otherwise).
- MailCare itself speaks plain HTTP. When it is used over the network, put a reverse proxy in front of it for
  HTTPS, let MailCare accept only the proxy (`--web-listen 127.0.0.1`) and make the proxy pass the `Host`
  header through unchanged.

### 1.6 Outbound connections

Besides the IMAP servers of the monitored addresses and the SMTP server of the notification mails (with the
delivery probe, also the sending server (SMTP) of each address and the probe server), MailCare connects to the
following.

- **DNS check (on by default).** MailCare sends plain, unencrypted DNS queries (UDP, and TCP for long answers;
  port 53) to the resolvers in "Settings -> General" (Cloudflare 1.1.1.1, Google 8.8.8.8 and Quad9 9.9.9.9 by
  default), or only to the operating system's resolver when so configured. It asks for:
  - a fixed control name (`example.com`), at most once per resolver and run, when the run first uses that
    resolver, to check that it answers;
  - the names whose lookup failed according to delivery-failure notices of the last two days (only names in the
    recipient's domain or among its mail servers), and the mail servers (MX) of those recipient domains;
  - the host name of the server that issued such a notice, to tell whether it is one of yours;
  - the SPF records of the domains of the monitored addresses and the names they refer to;
  - the addresses of your own sending servers (the operating system's resolver first).

  For a notice that clearly concerns mail you sent, the name servers of the domain concerned are also asked
  directly, unless only the operating system's resolver is used.
- **What the other side learns.** The resolvers, the name servers asked directly and the networks in between see
  this server's IP address, the time and the names asked. The names come from the notices, so whoever sends a
  notice to a monitored address can make this server look up names of their choosing, and by making the notice
  look like one about your mail, make it ask their own name servers directly. If this server's IP address must not
  be disclosed that way, switch the DNS check off.
- **With the DNS check off** ("Settings -> General", or `mailcare settings set dns_check_enabled 0`), the only DNS
  queries are the lookups of the host names of your own sending servers through the operating system's resolver.
  The results stored earlier still classify the notices. "Use only the OS resolver" keeps the check on but
  sends every query to the operating system's resolver and none to the name servers directly.
- **Stored results.** The results of the DNS check, with the answers of each resolver and name server, are stored
  in `data/` for the mail retention plus one day; the looked-up addresses of your own sending servers for up to
  90 days.
- **AI cause analysis.** When Codex CLI is installed and signed in, the analysis hands it the alert summary
  (including the recipient addresses), sample notices (headers, the headers of the returned mail, body excerpts
  and full bodies as files) and the DNS check results, and Codex CLI sends them to OpenAI. Switching automatic
  analysis off in "Settings -> General" stops the automatic runs; an administrator can still start an analysis by
  hand. To send nothing to OpenAI, do not install or sign in Codex CLI with the OS user that runs MailCare.

### 1.7 Command line

While the server is running, command line operations are handed to it. Main commands:

| Command | Meaning |
| --- | --- |
| `mailcare user create --username admin --role admin` | Create a user |
| `mailcare mailbox add --address a@example.com --host imap.example.com --username a@example.com` | Register a mail address to watch (the password is prompted for) |
| `mailcare sync --wait` | Sync every mail address now (fetch -> group -> analyze). Add `--all-time` to check every mail regardless of the period |
| `mailcare schedule set 06:00 12:00 18:00` | Change the automatic check times |
| `mailcare mailbox update a@example.com --smtp-host mail.example.jp` | Set the SMTP server (sending) of an address (`mailbox add` takes `--smtp-host` too; the connection the probe mails are sent through takes `--smtp-port`, `--smtp-security`, `--smtp-username` and `--smtp-password`; `--smtp-host ""` clears the whole connection; changing the connection while a password is stored needs `--smtp-password` too) |
| `mailcare settings show` / `mailcare settings set <key> <value>` | Show / change the settings |
| `mailcare settings set own_sending_hosts mail1.example.jp,mail2.example.jp` | Set your own sending servers that belong to no address (`own_sending_ipv4`, `own_sending_ipv6`, `own_sending_hosts`; comma-separated, `""` clears the list) |
| `mailcare settings set dns_check_enabled 0` | Switch the DNS check off (`dns_check_resolvers` sets the resolvers, `dns_check_system_resolver_only 1` uses only the operating system's resolver) |
| `mailcare cleanup` | Apply the retentions now: delete old notices and junk mails from the IMAP server, remove old mail, temporary files, agent workspaces and job history |
| `mailcare probe send` / `mailcare probe check` | Send the probe mails now / check the arrival of the missing ones now |
| `mailcare settings set probe_send_times 07:00,11:00,15:00,19:00` | Change a delivery probe setting (`probe_enabled`, `probe_address`, `probe_imap_host` and the other `probe_*` keys; the SMTP server of a mail address with `mailbox update A --smtp-host H --smtp-port N` and so on) |
| `mailcare jobs list` | Show the task history |

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
