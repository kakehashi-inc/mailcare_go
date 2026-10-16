# MailCare command-line reference

Japanese version: [command-line-reference-ja.md](command-line-reference-ja.md)

This reference describes every command of the `mailcare` executable: its options, the rules the values follow and
how the commands work together with a running server. The terms follow the Web screens (glossary:
[システム仕様.md](システム仕様.md) section 1). `mailcare --help` and `mailcare <command> --help` print a short form of
the commands and options.

## 1. Usage

### 1.1 Synopsis

```text
mailcare [--data-dir DIR] <command> [arguments] [options]
```

| Option | Meaning |
| --- | --- |
| `--data-dir DIR` | Data directory. Default: `data/` next to the executable. Accepted before or after the command; a relative path starts from the current directory |
| `-h`, `--help` | Show the help of the command (exit code 0) |

- Give the same `--data-dir` as the server (or none when the server runs without one). Section 2 depends on it.
- A command creates the data directory and its database when they do not exist. A mistyped `--data-dir` therefore
  yields a new, empty data directory instead of an error; the first line of `mailcare settings show` (`data_dir`) shows
  the directory in use.
- Times are shown in local time. `--json` output gives times in RFC 3339 (UTC).
- Errors are printed to standard error as `Error: <message>`, and the command ends with an exit code of section 14.
  A mistake in the command line itself (unknown command, missing required option, value outside an option's list)
  prints the usage and ends with exit code 2.

### 1.2 Commands

| Command | Purpose | Section |
| --- | --- | --- |
| `service start` / `stop` / `status` | Start, stop or inspect the server | 3 |
| `user ...` | Manage users | 4 |
| `token ...` | Manage API tokens (reserved for a future API) | 5 |
| `mailbox ...` | Manage the monitored mailboxes | 6 |
| `sync`, `fetch`, `group`, `reindex`, `reclassify`, `analyze`, `notify`, `cleanup`, `probe send`, `probe check` | Run a job now | 7 |
| `groups ...` | List the groups of a mailbox, show one, change its state | 8 |
| `schedule show` / `set` | Show or set the sync times | 9 |
| `settings show` / `set` | Show or change the settings | 10 |
| `jobs` / `jobs cancel` | Show the job history, cancel a queued job | 11 |
| `version` | Show the version | 12 |

## 2. Commands and a running server

### 2.1 Job commands: handed to the server or run in the command

The commands of section 7 run jobs. Before anything is queued, the command looks for a server of the same data
directory:

1. It reads the saved `web_port` (default 9790) and asks `http://127.0.0.1:<port>/control/status`, sending the control
   token that the server wrote to `control.token` in the data directory.
2. The answer decides where the jobs run:

| Situation | Behaviour | Output |
| --- | --- | --- |
| The server of this data directory answers | The jobs are queued on the server | `Server is running on port N; submitting to it.` |
| Nothing answers on the port | The jobs run in the process of the command, to the end | `Running <job> in this process...` |
| A server of another data directory answers | The jobs run in the process of the command, as when no server runs | `A server is running on port N for another data directory; running in this process.` |
| The server of this data directory refuses the control token | Nothing runs; exit code 5 | `... refused the control token of this command: run it as the OS user of the service with the same --data-dir, or restart the server` |

- Handing the jobs over keeps one process at a time writing to the mail index of a mailbox.
- An identical job (same kind, mailbox and target) that is still queued or running is not queued again. The server
  reports it (`Job #N (...) is already queued` or `... running`), and a waiting command follows that job. When the jobs run in the
  process of the command, such a job ends the command with an error; `mailcare jobs cancel ID` removes a queued one.

### 2.2 Waiting for the jobs

| Command | Handed to the server | Run in the process of the command |
| --- | --- | --- |
| `sync`, `cleanup` | Returns after queuing; waits only with `--wait` | Runs to the end |
| `fetch`, `group`, `reindex`, `reclassify`, `analyze`, `notify`, `probe send`, `probe check` | Always waits | Runs to the end |

- While waiting, the command prints the progress of each job and its outcome (`Job #N done: <result>`). It also
  follows the jobs the job caused: the per-mailbox jobs of a command given no address, and the analysis queued after a
  sync, a grouping, a rebuild of the index or a reclassification. Jobs queued meanwhile by the scheduler, the Web
  screens or other commands are not followed.
- The analysis after those jobs is queued only while automatic analysis is on (`agent_enabled`) and the CLI of the AI
  agent is installed.
- Ctrl+C while waiting for the server stops only the waiting; the jobs keep running on the server
  (`Stopped waiting; the jobs keep running on the server.`, exit code 1). Ctrl+C during a run in the process of the
  command cancels the job (exit code 1).
- When a followed job fails or is canceled, the command ends with exit code 5 (`N job(s) failed`).

### 2.3 Refused before queuing

A job that cannot do anything now is refused before anything is queued, with exit code 3:

| Command | Refused when |
| --- | --- |
| `analyze` | The CLI of the AI agent (`agent_provider`) is not installed, or a recorded usage limit of the agent lasts (the message names its end) |
| `probe send` | The delivery probe is off, the probe server is not set (probe address, IMAP host, username and password), or no enabled mailbox has the send check on |
| `probe check` | The delivery probe is off, the probe server is not set, or no probe mail is open (awaiting arrival, delayed, failure or send failed) |

The scheduler and the analysis queued after other jobs do not go through these checks.

### 2.4 Other commands

The other commands (`user`, `token`, `mailbox`, `groups`, `schedule`, `settings`, `jobs`) read and write the database
of the data directory directly, whether a server runs or not. The running server reads most changes when it next uses
them, with these exceptions:

- `schedule set` and `settings set check_times`: the scheduler reads the new sync times within 30 seconds.
- `settings set web_listen`, `web_port`, `cookie_ttl_hours` and `workers`: take effect at the next start of the server
  (the Web settings change the concurrent jobs immediately).

### 2.5 OS user, host and data directory

- The control endpoints answer only on the loopback address: run the commands on the machine of the server.
- Only the OS user that runs the server can read `control.token` (permission 0600). Run the commands as that OS user
  with the same `--data-dir`, for example `sudo -u mailcare /opt/mailcare/mailcare service stop`.
- Run as another OS user, the commands fail: the database cannot be opened (exit code 3), or the server refuses the
  control token (2.1, exit code 5; also for `service stop` and `service status`). A job command never runs the jobs in
  the process of the command while the server of its data directory is up.

## 3. Server: service

### 3.1 service start

```text
mailcare service start [--web-listen ADDR] [--web-port N] [--workers N] [--check-time HH:MM ...] [--mail-keep-days N]
```

Starts the server (Web screens, job workers and scheduler) in the foreground. Ctrl+C or SIGTERM stops it.

| Option | Meaning | Default |
| --- | --- | --- |
| `--web-listen ADDR` | Listen address: IP address or host name | Saved value, else `0.0.0.0` |
| `--web-port N` | Listen port, 1-65535 | Saved value, else 9790 |
| `--workers N` | Concurrent jobs, 1-16 | Saved value, else 2 |
| `--check-time HH:MM` | Sync time (local time). Repeat the option or separate times with commas | Saved value, else `06:00,12:00,18:00` |
| `--mail-keep-days N` | Mail retention in days, 1-3650 | Saved value, else 180 |

- Every value given is saved as the setting of the same name (`web_listen`, `web_port`, `workers`, `check_times`,
  `mail_keep_days`), so later starts can omit it. The listen address and port are saved before the port is opened: when
  the start fails because of them, start again with correct values.
- A value out of range or in the wrong form ends the command with exit code 2 before the server starts.
- One server per data directory: a second start fails with `another MailCare server is running for this data
  directory` (exit code 5) without touching the database. The server holds the lock of `server.lock` in the data
  directory and removes the file when it stops; a file left by a crash does not block the next start.
- Stopping first stops the scheduler and the running jobs (their mail is handled again by the next run), tells the
  requests in progress that the server stops, then waits for them (downloads included) to end, without a time limit.
  A second Ctrl+C (SIGINT or SIGTERM) while it waits exits at once.
- When the listen address is a specific address (not `0.0.0.0`, `::`, `127.0.0.1`, `::1` or `localhost`), the server
  also listens on `127.0.0.1` at the same port for the commands of section 2. With `::1` the commands cannot reach the
  server, because they connect to `127.0.0.1`.
- When the server creates the default API token, the log shows its value once. With no user yet, the log asks to
  create the first administrator on the Web setup screen or with `user create --role admin`.
- To keep the server running, register it as an OS service ([README.md](../README.md) section 1.3).

### 3.2 service stop

```text
mailcare service stop [--web-port N]
```

Stops the running server of this data directory.

| Option | Meaning | Default |
| --- | --- | --- |
| `--web-port N` | Port of the running server | Saved `web_port`, else 9790 |

- A server of another data directory on that port is left running
  (`the server on port N serves another data directory: <dir>`, exit code 5).
- No server on the port, or a refused control token: exit code 5.
- The command returns once the server has taken the request (`Server shutdown initiated`). The server then stops as
  under `service start`: the scheduler and the running jobs first, then it waits for the requests in progress.

### 3.3 service status

```text
mailcare service status [--web-port N]
```

Prints the status of the running server: `status`, `name`, `version`, `web_listen`, `data_dir`, `uptime`, `users`,
`tokens`, `mailboxes`, `active_jobs` and `next_check` (next sync, local time). `--web-port` as for `service stop`.
No server on the port, or a refused control token: exit code 5.

## 4. Users: user

Users log in to the Web screens. A username has 1-64 characters of `A-Z a-z 0-9 _ . -`. A password has at least
8 characters and at most 72 bytes (section 13).

### 4.1 user create

```text
mailcare user create --username NAME [--display-name NAME] [--email ADDRESS] [--language ja|en] [--timezone TZ] [--theme auto|light|dark] [--role admin|user] [--password PASSWORD]
```

Creates a user. `mailcare user create --username admin --role admin` creates the first administrator without the Web
setup screen.

| Option | Meaning | Default |
| --- | --- | --- |
| `--username NAME` | Username (required) | - |
| `--display-name NAME` | Display name, up to 128 characters | The username |
| `--email ADDRESS` | Email address for the notification emails; unique among the users | None |
| `--language ja\|en` | Language of the screens and of the emails to the user | `ja` |
| `--timezone TZ` | Time zone, IANA name (`Asia/Tokyo`, `UTC`, ...) | `Asia/Tokyo` |
| `--theme auto\|light\|dark` | Theme | `auto` |
| `--role admin\|user` | Role: administrator or member | `user` |
| `--password PASSWORD` | Password | Asked for (section 13) |

### 4.2 user list

```text
mailcare user list [--json]
```

Lists the users: ID, username, display name, email address, time zone, role and last login. `--json` adds language,
theme and creation time. The ID is what `notify_user_ids` takes (10.7).

### 4.3 user update

```text
mailcare user update USERNAME [--display-name NAME] [--email ADDRESS] [--language ja|en] [--timezone TZ] [--theme auto|light|dark]
```

Changes the profile of a user; options not given stay unchanged, and at least one is required (exit code 2
otherwise). `--display-name ""` restores the username, `--email ""` removes the email address, and an empty language,
time zone or theme restores its default.

### 4.4 user passwd

```text
mailcare user passwd USERNAME [--password PASSWORD]
```

Sets a new password (asked for when `--password` is omitted). The Web sessions of the user end.

### 4.5 user role

```text
mailcare user role USERNAME --role admin|user
```

Changes the role. The last administrator cannot become a member (exit code 2).

### 4.6 user delete

```text
mailcare user delete USERNAME [-y]
```

Deletes a user after a `[y/N]` confirmation; `-y` skips it. The last administrator cannot be deleted (exit code 2).

## 5. API tokens: token

Tokens are reserved for a future API. They belong to no user and do not log in to the Web screens. A token value
starts with `mlc_`.

### 5.1 token create

```text
mailcare token create --identifier ID [--name NAME] [--expires EXPIRY] [--json]
```

Creates a token and prints its value.

| Option | Meaning | Default |
| --- | --- | --- |
| `--identifier ID` | Identifier (required): 1-64 characters of `A-Z a-z 0-9 _ -`, unique | - |
| `--name NAME` | Display name | The identifier |
| `--expires EXPIRY` | Expiry: a duration from now (`720h`, `90m`; units `h`, `m`, `s`), an RFC 3339 time, or `YYYY-MM-DD HH:MM:SS` / `YYYY-MM-DD` (UTC) | Never |
| `--json` | JSON output, value included | - |

A token created while no token exists becomes the default token.

### 5.2 token list

```text
mailcare token list [--json]
```

Lists identifier, name, default, expiry and creation time, without the values.

### 5.3 token show

```text
mailcare token show ID [--json]
```

Shows one token with its value.

### 5.4 token delete

```text
mailcare token delete ID [-y]
```

Deletes a token after a `[y/N]` confirmation; `-y` skips it. Deleting the default token makes no other token the
default: at its next start the server makes the token with the identifier `default` the default, or creates one.

## 6. Mailboxes: mailbox

A mailbox is a monitored email address with its IMAP connection. Addresses are stored in lower case; the commands find
a mailbox by its address in any case.

### 6.1 mailbox add

```text
mailcare mailbox add --address ADDRESS --host HOST --username USER [--port N] [--security ssl|starttls|none] [--password PASSWORD] [--folder FOLDER] [--initial-days N] [--recent-days N] [--server-keep-days N] [--smtp-host HOST] [--display-name NAME] [--disabled] [send check options (6.2)]
```

Registers a mailbox, then tests the IMAP connection.

| Option | Meaning | Default |
| --- | --- | --- |
| `--address ADDRESS` | Email address to monitor (required) | - |
| `--host HOST` | IMAP server: host name or IP address (IPv6 such as `::1` too; not `host:port`) (required) | - |
| `--username USER` | IMAP username (required) | - |
| `--port N` | IMAP port, 1-65535 | 993, also with `starttls` |
| `--security ssl\|starttls\|none` | Connection security | `ssl` |
| `--password PASSWORD` | IMAP password | Asked for (section 13) |
| `--folder FOLDER` | Folder to fetch | `INBOX` |
| `--initial-days N` | Initial fetch days: how far back the first sync looks, 1-3650 | 90 |
| `--recent-days N` | Regular fetch days: how far back later syncs look, 1-3650 | 10 |
| `--server-keep-days N` | Server retention in days, 0-3650 (0 = never delete on the IMAP server), at most `mail_keep_days` | 60, or `mail_keep_days` when shorter |
| `--smtp-host HOST` | SMTP server that sends the mail of this address (a sending source) | Not set |
| `--display-name NAME` | Display name (also the sender name of the probe mails), up to 128 characters | None |
| `--disabled` | Register with monitoring off (no automatic sync) | Monitoring on |

- The address must be new, also in the form used for its directory name: `a!b@example.com` and `a_b@example.com`
  share `a_b@example.com`, so the second one is refused.
- `--smtp-host` is the name that SMTP server uses in the `Received` headers it adds: letters, digits, hyphens and
  dots; IP addresses and `localhost` are refused. It is not the SMTP server of the notification emails (10.7). It
  serves the judgement of the sending sources, and the send check (6.2) sends through it.
- The fetch days never reach back beyond the mail retention (`mail_keep_days`).
- The server retention counts from the date of a mail: target mail and junk mail older than that are deleted from the
  IMAP server by the daily cleanup. The deletion cannot be undone; use 0 for a mailbox whose mail must stay.
- A failed connection test keeps the mailbox and prints a warning (exit code 0). Correct the settings with
  `mailbox update` and check them with `mailbox test`.

### 6.2 Send check options (mailbox add and mailbox update)

```text
[--probe on|off] [--smtp-port N] [--smtp-security ssl|starttls|none] [--smtp-username USER] [--smtp-password PASSWORD | --ask-smtp-password]
```

With the send check on, the delivery probe sends the probe mails of this address through its SMTP server
(`--smtp-host`).

| Option | Meaning | Default |
| --- | --- | --- |
| `--probe on\|off` | Send check on or off | `add`: off; `update`: unchanged |
| `--smtp-port N` | SMTP port, 1-65535 (587, for example); required with the send check on | - |
| `--smtp-security ssl\|starttls\|none` | Connection security | `starttls` |
| `--smtp-username USER` | SMTP username; `""` = no authentication | No authentication |
| `--smtp-password PASSWORD` | SMTP password; the value stays in the shell history and the process list | - |
| `--ask-smtp-password` | Ask for the SMTP password (section 13) | - |

- With the send check on, `--smtp-host` and `--smtp-port` are required.
- `--smtp-port`, `--smtp-security`, `--smtp-username`, `--smtp-password` and `--ask-smtp-password` need the send check
  on after the command (turned on now, or already on for `update`); otherwise exit code 2.
- `--probe off` keeps only the SMTP host and removes port, security, username and password.
- With a username, a usable password is required. `mailbox add --probe on --smtp-username USER` asks for it when no
  password option is given. On `update`, the stored password is kept only while SMTP host, port, security and username
  stay the same; changing any of them needs `--smtp-password` or `--ask-smtp-password` (otherwise exit code 2, nothing
  saved). An empty username drops the password.
- `--smtp-security none` with a username prints a warning: the username and password travel in plain text.
- Turning the send check off, or turning the monitoring of the mailbox off, resolves its open probe mails.

### 6.3 mailbox list

```text
mailcare mailbox list [--json]
```

Lists the mailboxes by domain, then address: ID, address, IMAP server, enabled, last fetch and its status (`never`,
`ok`, `error`). `--json` adds the other settings of each mailbox, without the passwords (`smtp_password_set` tells
whether an SMTP password is stored).

### 6.4 mailbox show

```text
mailcare mailbox show ADDRESS [--json]
```

Shows one mailbox: IMAP connection, folder, enabled, fetch days, server retention, `smtp host` (`(not set)` when
empty), `send check` (`on: port N (security) as USER`, or `off`), last fetch and last error.

### 6.5 mailbox update

```text
mailcare mailbox update ADDRESS [--new-address ADDRESS] [--host HOST] [--port N] [--security ssl|starttls|none] [--username USER] [--password PASSWORD | --ask-password] [--folder FOLDER] [--initial-days N] [--recent-days N] [--server-keep-days N] [--smtp-host HOST] [--display-name NAME] [--enabled | --disabled] [send check options (6.2)]
```

Changes the options given; the others stay unchanged. The options are those of `mailbox add`, plus:

| Option | Meaning |
| --- | --- |
| `--new-address ADDRESS` | Rename the mailbox |
| `--password PASSWORD` / `--ask-password` | New IMAP password, given or asked for (section 13) |
| `--smtp-host ""` | Remove the SMTP host (needs `--probe off` while the send check is on) |
| `--enabled` / `--disabled` | Monitoring on or off (not both) |

- The stored IMAP password is kept only while host, port, security and username stay the same. Changing any of them
  needs `--password` or `--ask-password`; otherwise exit code 2
  (`imap_password is required when the connection settings change`) and nothing is saved. Changing only
  `--smtp-host` needs no IMAP password.
- `--server-keep-days` must not exceed `mail_keep_days`. A stored value above it (left from before `mail_keep_days`
  was shortened) is accepted unchanged; the cleanup then uses the shorter of the two.
- A rename moves the original messages, the mail index and the agent working directories to the new name. It is
  refused while a job of the mailbox runs (exit code 5), when another mailbox has the new address, and when data under
  the new name exists already.
- An empty `--display-name` leaves the display name unchanged; `--port`, `--initial-days` and `--recent-days` with 0
  leave the value unchanged.

### 6.6 mailbox delete

```text
mailcare mailbox delete ADDRESS [-y] [--keep-data]
```

Deletes a mailbox after a `[y/N]` confirmation; `-y` skips it.

- Without `--keep-data`, the original messages, the mail index (groups and analysis reports) and the agent working
  directories are deleted too; `--keep-data` leaves them in the data directory.
- The queued jobs of the mailbox are canceled. While a job of the mailbox runs, the deletion is refused (exit code 5).
- The probe mail records of the mailbox are deleted. The job history stays (11.1). Mail on the IMAP server is not
  touched.

### 6.7 mailbox test

```text
mailcare mailbox test ADDRESS
```

Connects with the stored settings, logs in and opens the folder. Prints `ok`; on failure, prints the error and ends
with exit code 5.

## 7. Jobs on demand

These commands run a job now, on a running server or in the process of the command (section 2). Without an address,
`sync` and `fetch` cover every enabled mailbox, and `group`, `reindex`, `reclassify`, `analyze` and `cleanup` every
mailbox, enabled or not. An address given by name is processed even when its monitoring is off.

### 7.1 sync

```text
mailcare sync [ADDRESS ...] [--wait] [--all-time]
```

Sync: fetches new mail, groups it and queues the analysis of the groups that need one.

| Option | Meaning |
| --- | --- |
| `--wait` | When handed to the server, wait for the jobs and show their progress (2.2) |
| `--all-time` | Search the whole folder instead of the fetch days (which never reach back beyond the mail retention) |

With `--all-time`, mail older than the mail retention is fetched too. The next cleanup deletes the target mail and
junk mail among it from the IMAP server (when the server retention is above 0, 6.1) and then removes all of it locally.

### 7.2 fetch

```text
mailcare fetch [ADDRESS ...] [--all-time]
```

Fetch only: stores new mail without grouping it (it waits as "Awaiting grouping"). `--all-time` as for `sync`.

### 7.3 group

```text
mailcare group [ADDRESS ...]
```

Grouping only: classifies and groups the mail not grouped yet, then queues the analysis. (`groups`, section 8, lists
the groups.)

### 7.4 reindex

```text
mailcare reindex [ADDRESS ...]
```

Rebuild index: builds the mail index again from the original messages and classifies and groups every mail again,
then queues the analysis. Groups that come back keep their state, state history and analysis reports.

### 7.5 reclassify

```text
mailcare reclassify [ADDRESS ...]
```

Reclassify: classifies and groups every fetched mail again, then queues the analysis. Run it after an upgrade that
changes the classification and after a change of the sending sources or the DNS check. Groups keep their state and
reports; a group left without mail is deleted with its reports and history. Mail that becomes junk can be deleted from
the IMAP server by the next cleanup: check the Junk tab first.

### 7.6 analyze

```text
mailcare analyze [ADDRESS] [--group KEY | --all]
```

Runs the analysis by the AI agent over the groups of one mailbox, or of every mailbox without ADDRESS.

| Option | Groups analyzed |
| --- | --- |
| (none) | The actionable groups waiting for analysis |
| `--all` | Every actionable group |
| `--group KEY` | One group, also an excluded one (needs ADDRESS) |

- `--group` and `--all` exclude each other; `--group` without ADDRESS: exit code 2.
- Refused with exit code 3 when the CLI of the agent is not installed or a usage limit lasts (2.3).
- When the agent reports its usage limit during the job, the job ends with an error and the groups not analyzed wait
  for the analysis after the limit (with `--group` and `--all` too; an excluded group asked for with `--group` as
  well).

### 7.7 notify

```text
mailcare notify [--test [--to ADDRESS]]
```

Without options: sends the notification email now to the notification recipients (`notify_user_ids` with an email
address), even while `notify_enabled` is off, and records the send. With nothing to send, the job ends with
`skipped: <reason>` (exit code 0).

| Option | Meaning | Default |
| --- | --- | --- |
| `--test` | Send a short test mail with the saved SMTP settings instead of the notification | - |
| `--to ADDRESS` | Recipient of the test mail (needs `--test`) | Every notification recipient with an email address, one mail each |

- `--test` without `--to` while no notification recipient has an email address: exit code 2.
- The test mail takes the language and time zone of the user whose email address receives it; for any other address,
  Japanese and the local time of the server.

### 7.8 cleanup

```text
mailcare cleanup [ADDRESS ...] [--wait]
```

Applies the retentions now, as the daily cleanup at `cleanup_time` does. For each mailbox, in this order:

1. On an enabled mailbox with a server retention above 0: deletes target mail and junk mail older than the shorter of
   the server retention and the mail retention from the IMAP server (cannot be undone).
2. Removes the mail older than the mail retention (`mail_keep_days`); groups left without mail are deleted.
3. Removes temporary files left by interrupted writes (older than 1 day).
4. Removes agent working directories older than `agent_keep_days`.
5. Removes probe mail records older than the mail retention (7 days at least).
6. Removes jobs finished more than 30 days ago, and stored DNS check results and reporting MTA lookups past their
   retention.

`--wait` as for `sync` (2.2).

### 7.9 probe send / probe check

```text
mailcare probe send
mailcare probe check
```

- `probe send`: sends a probe mail now from every enabled mailbox with the send check on to the probe address. The
  send is recorded as one by hand (no send time); a running server checks the arrival after the delay threshold.
- `probe check`: looks for the open probe mails on the probe server now and updates their state.
- Both always wait, and both are refused with exit code 3 when nothing can be done (2.3).

## 8. Groups: groups

### 8.1 groups list

```text
mailcare groups [list] ADDRESS [--state open|resolved|ignored] [--recheck] [--scope actionable|excluded|all] [--category CATEGORY] [--json]
```

Lists the groups of a mailbox; `list` may be omitted.

| Option | Meaning | Default |
| --- | --- | --- |
| `--state STATE` | Only groups in this state | Every state |
| `--recheck` | Only resolved or ignored groups sent back for a re-check | - |
| `--scope SCOPE` | `actionable`: the groups of the Alerts screen; `excluded`: the open groups of recipient-side problems; `all`: both | `actionable` |
| `--category CATEGORY` | Only this category | Every category |
| `--json` | JSON output with the summary of the latest report | - |

- Columns: KEY, STATE (`(re)` marks a re-check), CATEGORY, RESP (responsible party), MSGS, LAST SEEN, SEVERITY (of the
  latest completed report), TITLE.
- Categories: `ip_blocked`, `rate_limited`, `sender_blocked`, `auth_failure`, `content_rejected`, `message_too_large`,
  `server_config`, `dns_resolver_failure`, `unknown_failure`, `dmarc_spf_missing`, `dmarc_dkim_failed`,
  `dmarc_not_authenticated`, `user_unknown`, `mailbox_full`, `mailbox_disabled`, `domain_not_found`,
  `dns_failure_external`, `delivery_delay`.

### 8.2 groups ADDRESS KEY

```text
mailcare groups [list] ADDRESS KEY [--json]
```

Shows one group: label, state with its action taken or reason for ignoring and details, category, action unit,
decided by, actionable, recipient domain, status code, responsible party, counts, first and last seen, waiting for
analysis, diagnostic template, recipients, remote IPs and MTAs, the state history (newest first) and the latest
completed analysis report. Mail bodies are not shown; the filter options of 8.1 do not apply.

### 8.3 groups set-state

```text
mailcare groups set-state ADDRESS KEY open|resolved|ignored [--reason CODE] [--note TEXT]
```

Changes the state of a group, as the Alerts screen does. The state history records the change with an empty
"changed by".

| Option | Meaning |
| --- | --- |
| `--reason CODE` | Action taken (`resolved`) or reason for ignoring (`ignored`); required for both |
| `--note TEXT` | Details: what was done (`resolved`), or the reason in words (`ignored` with `--reason other`) |

| Action taken (`resolved`) | Meaning |
| --- | --- |
| `delisting` | Requested delisting from the blacklist |
| `dns_fixed` | Corrected the DNS records of the sending domain (SPF, DKIM, DMARC) |
| `server_fixed` | Corrected the sending server configuration |
| `sender_changed` | Reviewed the sender address |
| `content_changed` | Reviewed the content or attachments |
| `volume_adjusted` | Adjusted the sending volume or rate |
| `recipient_fixed` | Corrected or removed the recipient address |
| `recipient_asked` | Contacted the recipient domain administrator |
| `other` | Other |

| Reason for ignoring (`ignored`) | Meaning |
| --- | --- |
| `temporary` | A temporary problem that has already cleared |
| `recipient_side` | A problem on the recipient side |
| `input_error` | The recipient address was entered incorrectly |
| `stopped_sending` | Mail is no longer sent to this address |
| `spoofing` | Sent by a third party impersonating the monitored domain |
| `external_service` | A problem with an external sending service |
| `false_positive` | Detected by mistake |
| `low_impact` | The impact is small |
| `test_mail` | Test or verification mail |
| `other` | Other |

- `open` ignores `--reason` and `--note` and clears the reason and details of the group (the history keeps them).
- For `ignored`, `--note` is kept only with `--reason other`.
- Setting `resolved` or `ignored` again on a group sent back for a re-check records the new decision.

## 9. Sync times: schedule

### 9.1 schedule show

```text
mailcare schedule show [--json]
```

Shows the sync times and the next sync (local time; syncs run only while the server runs).

### 9.2 schedule set

```text
mailcare schedule set [HH:MM ...]
```

Replaces the sync times (local time). `H:MM` and comma-separated times are accepted; the times are sorted and
duplicates removed. Without a time, automatic sync is off. The word `none` is not accepted here (exit code 2);
`settings set check_times none` accepts it. A running server reads the new times within 30 seconds.

## 10. Settings: settings

### 10.1 settings show

```text
mailcare settings show [--json]
```

Shows the effective value of every setting of 10.3-10.8 (saved value, else default) and these values, which cannot be
set:

| Value | Meaning |
| --- | --- |
| `data_dir` | Data directory of the command |
| `smtp_password`, `probe_imap_password` | `(set)` or `(not set)`; JSON: `smtp_password_set`, `probe_imap_password_set`. Passwords are never shown |
| `effective_base_url` | URL used in the notification emails while `public_base_url` is empty |
| `notify_last_sent_at`, `next_send_at` | Last notification sent; next scheduled notification, the next of the notification times on a notification day (none while notifications are off) |
| `sending_source_mailbox_hosts` | SMTP hosts of the mailboxes (set with `mailbox update --smtp-host`) |
| `sending_source_mailboxes_without_host` | Text: `mailboxes without a host name`. Mailboxes without an SMTP host while `sending_source_hosts` is empty; bounces of mail not sent from the sending sources are not recognized for them |
| `probe_last_send_at`, `probe_next_check_at` | Last probe send; next arrival check |
| `probe_last_check_error`, `probe_last_delete_error` | Last errors of the arrival check (text output only, when present) |
| `cleanup_last_run_date` | Date of the last daily cleanup |
| `providers` | JSON only: the agent providers and whether each CLI is installed |

The text output adds `(available)` or `(not available)` to `agent_provider`, and
`(not used: only the OS resolver is queried)` to `dns_check_resolvers` while `dns_check_system_resolver_only` is on.

### 10.2 settings set

```text
mailcare settings set KEY [VALUE]
```

Changes one setting.

- VALUE is required, except for `smtp_password` and `probe_imap_password`, which are asked for when it is omitted
  (section 13).
- An empty VALUE (`""`) restores the default. The default passes the same checks as any value, so a default that
  breaks a rule shared with other settings is refused (`smtp_host ""` while notifications are on, for example).
- Values are checked as on the Web settings screens. A refused value ends the command with exit code 2 and saves
  nothing.
- Booleans: `1` / `0`, `true` / `false`, `yes` / `no`, `on` / `off`. Lists: comma-separated; a list replaces the saved
  one.
- An unknown key is refused with the list of known keys (exit code 2); the values of 10.1 and the internal
  `secret_key` cannot be set.
- The output shows the value stored (`KEY = VALUE`) or `KEY restored to its default`.

### 10.3 Server

| Key | Value | Default |
| --- | --- | --- |
| `web_listen` | Listen address: IP address (IPv6 with or without brackets) or host name | `0.0.0.0` |
| `web_port` | Listen port, 1-65535 | 9790 |
| `workers` | Concurrent jobs, 1-16 | 2 |
| `cookie_ttl_hours` | Lifetime in hours of a login with "Stay logged in"; a positive integer | 720 |

- These take effect at the next start of the server (2.4).
- After changing `web_port` while the server runs, restart the server before running job commands: until then they
  look for the server on the new port and run the jobs in the process of the command.
  `service stop --web-port <old port>` reaches the running server.

### 10.4 Schedule, retention and re-check

| Key | Value | Default |
| --- | --- | --- |
| `check_times` | Sync times: comma-separated `HH:MM` (local time); `none` = no automatic sync | `06:00,12:00,18:00` |
| `cleanup_time` | Time of the daily cleanup: one `HH:MM` (local time) | `02:00` |
| `mail_keep_days` | Mail retention in days, 1-3650 | 180 |
| `recheck_days` | Days before re-check, per category: comma-separated `CATEGORY=DAYS`, 1-365 days | Per category (below) |

- Shortening `mail_keep_days` leaves the server retention of the mailboxes as it is; the cleanup uses the shorter of
  the two.
- `recheck_days`: a resolved group goes back for a re-check when a notice arrives about mail sent more than this many
  days after the group was resolved (the time the action needs to take effect). Categories not named keep their
  value; `""` restores the default of every category. Defaults: 14 days for `ip_blocked` and `sender_blocked`; 3 days
  for `auth_failure`, `content_rejected`, `message_too_large`, `server_config` and `dns_resolver_failure`; 7 days for
  the others. The categories are listed in 8.1.

### 10.5 Sending sources and DNS check

| Key | Value | Default |
| --- | --- | --- |
| `sending_source_ipv4` | Sending source IPv4: IPv4 addresses and CIDRs | None |
| `sending_source_ipv6` | Sending source IPv6: IPv6 addresses and CIDRs | None |
| `sending_source_hosts` | Sending source host names | None |
| `dns_check_enabled` | DNS check on or off | On |
| `dns_check_resolvers` | Resolvers: IPv4 / IPv6 addresses, up to 10; `none` = an empty list | `1.1.1.1,8.8.8.8,9.9.9.9` |
| `dns_check_system_resolver_only` | Use only the OS resolver | Off |

- The sending source lists take up to 500 entries each and are normalized as on the Web screen (canonical IP form,
  CIDR aligned to its network address, a one-address CIDR stored as the address, host names in lower case without a
  trailing dot). Refused: zones, loopback and unspecified addresses; host names that are IP addresses, contain `_` or
  are `localhost`. `""` empties the list.
- The SMTP hosts of the mailboxes are sending sources too; `settings show` lists them, and `mailbox update --smtp-host`
  changes them.
- The three DNS check keys are checked together with the saved values of the other two: an enabled DNS check that does
  not use only the OS resolver needs at least one resolver (otherwise exit code 2, nothing saved). A value of commas
  and blanks only is refused; `none` saves an empty list.
- After changing the sending sources or the DNS check, run `mailcare reclassify` (7.5).

### 10.6 AI agent

| Key | Value | Default |
| --- | --- | --- |
| `agent_provider` | Provider of the agent CLI (registered now: `codex`) | `codex` |
| `agent_model` | Model passed to the CLI; `""` = the default of the CLI | Default of the CLI |
| `agent_reasoning_effort` | Reasoning level passed to the CLI; `""` = the setting of the CLI | Setting of the CLI |
| `agent_enabled` | Automatic analysis on or off | On |
| `agent_keep_days` | Working directory retention in days, 1-365 | 30 |

- Changing `agent_provider` clears `agent_model` and `agent_reasoning_effort`.
- `agent_model` is refused when its form is invalid or the model does not accept the saved reasoning level (change
  `agent_reasoning_effort` first).
- `agent_reasoning_effort` is refused when its form is invalid, the provider has no reasoning level, or the saved model
  does not accept it.

### 10.7 Notifications and SMTP

| Key | Value | Default |
| --- | --- | --- |
| `smtp_host` | SMTP host of the notification emails: host name or IP address | None |
| `smtp_port` | 1-65535 | 587 |
| `smtp_security` | `ssl`, `starttls` or `none` | `starttls` |
| `smtp_username` | SMTP username; `""` = no authentication | None |
| `smtp_password` | SMTP password, stored encrypted; omit VALUE to be asked for it | None |
| `smtp_from` | From address | None |
| `public_base_url` | Public URL for the links in the emails: absolute `http` / `https` URL without query or fragment | `http://<web_listen or localhost>:<web_port>` |
| `notify_enabled` | Notifications on or off | Off |
| `notify_time` | Notification times: comma-separated `HH:MM` (local time), sent at each of them on a notification day; `""` = the default | `09:00,17:00` |
| `notify_interval_days` | Interval in days, 1-7 | 1 |
| `notify_user_ids` | Recipients: comma-separated user IDs (`user list`), up to 100 | None |

- `notify_enabled` can be turned on only while `smtp_host` and `smtp_from` are set, and neither can be emptied while
  it is on.
- A notification day is a day with no notification sent yet, the day of the last notification, or a day at least
  `notify_interval_days` days after it; on such a day a notification goes out at each of the `notify_time` times.
- `smtp_password` needs `smtp_username` first. `smtp_password ""` is refused while a username is set; to stop
  authenticating, run `settings set smtp_username ""`, which removes the password too.
- While a password is stored, changing `smtp_host`, `smtp_port`, `smtp_security` or `smtp_username` to another
  non-empty value needs the password again: the command asks for it and saves both together. Setting a username while
  no password is stored asks for the password as well.
- A trailing `/` of `public_base_url` is removed.

### 10.8 Delivery probe

| Key | Value | Default |
| --- | --- | --- |
| `probe_enabled` | Delivery probe on or off | Off |
| `probe_address` | Probe address: receives the probe mails | None |
| `probe_imap_host` | Probe server: IMAP host name or IP address (not `host:port`) | None |
| `probe_imap_port` | 1-65535 | 993 |
| `probe_imap_security` | `ssl`, `starttls` or `none` | `ssl` |
| `probe_imap_username` | IMAP username | None |
| `probe_imap_password` | IMAP password, stored encrypted; omit VALUE to be asked for it; `""` removes it | None |
| `probe_imap_folder` | Folder (no line break) | `INBOX` |
| `probe_send_times` | Send times: comma-separated `HH:MM` (local time); `none` = no scheduled send | `07:00,11:00,15:00,19:00` |
| `probe_delay_minutes` | Delay threshold in minutes, 1-10080 | 15 |
| `probe_failure_minutes` | Failure threshold in minutes, 1-10080 | 60 |
| `probe_subject_tag` | Identifier: 1-16 letters, digits, `.`, `_` or `-` | `MC` |

- `probe_enabled` can be turned on only with probe address, IMAP host, username and password set; while it is on,
  none of the four can be emptied. Turning it off resolves the open probe mails.
- Do not use a monitored mailbox as the probe address: the probe mails would be fetched as its mail.
- Send times: up to 24, at least 15 minutes apart (across midnight too). The delay threshold must be shorter than the
  shortest gap between two send times (24 hours with one send time); the failure threshold must be longer than the
  delay threshold.
- While a password is stored, a change of IMAP host, port, security or username is refused (no prompt). Steps:
  `settings set probe_enabled 0`, `settings set probe_imap_password ""`, change the connection,
  `settings set probe_imap_password` (asked for), `settings set probe_enabled 1`.
- Several MailCare servers that share one probe server need different identifiers.
- `probe_imap_security none` with a username or password prints a warning: they travel in plain text.
- The account of the probe server needs the right to delete mail.

## 11. Job history: jobs

### 11.1 jobs list

```text
mailcare jobs [list] [--limit N] [--json]
```

Shows the job history, newest first (`--limit`, default 20; `list` may be omitted).

- Columns: ID, KIND, MAILBOX, STATUS, CREATED, RESULT / ERROR.
- Kinds: `sync`, `fetch`, `group`, `analyze`, `reindex`, `reclassify`, `notify`, `cleanup`, `probe_send`,
  `probe_check`. States: `queued`, `running`, `done`, `error`, `canceled`.
- MAILBOX shows `(all)` for a job that queues one job per mailbox and `(deleted mailbox)` for a job of a mailbox
  deleted since.
- `--json` adds target, `parent_id` (the job that queued it; `null` for a job queued directly), progress,
  `requested_by` (`cli`, `scheduler` or `web:<username>`) and the start and finish times.

### 11.2 jobs cancel

```text
mailcare jobs cancel ID
```

Cancels a queued job, with or without a running server. A running or finished job cannot be canceled (exit code 5);
an unknown ID ends with exit code 2.

## 12. version

```text
mailcare version
```

Prints the version (`dev` for a development build).

## 13. Password input

- A password given as an option value (`--password`, `--smtp-password`) stays in the shell history and the process
  list. Omit it, or use `--ask-password` / `--ask-smtp-password`, to type it instead.
- The command asks for a password in these cases: `user create` and `user passwd` without `--password`; `mailbox add`
  without `--password` (IMAP); `mailbox update --ask-password`; `--ask-smtp-password`;
  `mailbox add --probe on --smtp-username USER` without an SMTP password; `settings set smtp_password` and
  `settings set probe_imap_password` without VALUE; an SMTP connection change by `settings set` that needs the
  password (10.7).
- Every prompt asks twice and compares the answers. On a terminal the input is hidden. On piped input each answer is
  one line, so the input carries two lines per password, for example
  `printf '%s\n%s\n' "$PW" "$PW" | mailcare user passwd admin`.
- Different answers, or a missing second line: exit code 2. An empty answer to `settings set smtp_password` or
  `probe_imap_password`: exit code 2 (`settings set KEY ""` removes a stored password).
- User passwords: at least 8 characters, at most 72 bytes. The passwords of the mail servers are stored encrypted with
  a key kept in the database; `settings show` and `mailbox show` only tell whether one is stored.

## 14. Exit codes

| Code | Name in `app/modules/errors.go` | Meaning | Examples |
| --- | --- | --- | --- |
| 0 | `ExitSuccess` | Success | Also: help output; `mailbox add` with a failed connection test; a job that ends `skipped` |
| 1 | `ExitGeneral` | Unspecified error | Ctrl+C during a job command; an unexpected database error |
| 2 | `ExitArgument` | Invalid arguments or values | Command-line mistakes; a refused value; unknown user, mailbox, token, group or job; password answers that differ |
| 3 | `ExitConfig` | Configuration or database error | The database cannot be opened; a job refused before queuing (2.3); a setting that cannot be saved for another reason than its value |
| 4 | `ExitAuth` | Authentication or authorization failure | Defined; no command returns it now |
| 5 | `ExitExec` | Server or execution error | The server cannot start (another server of the data directory runs); no server for `service stop` / `status`; a refused control token; a failed job; a failed `mailbox test`; a mailbox with a running job; `jobs cancel` of a job that is not queued |
| 6 | `ExitFileIO` | File or directory error | The data directory cannot be created; the mail index of a mailbox cannot be opened (`groups`) |
| 7 | `ExitTimeout` | Operation timed out | Defined; no command returns it now |
