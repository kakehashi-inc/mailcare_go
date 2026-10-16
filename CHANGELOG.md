# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Several notification times can be set (09:00 and 17:00 by default); on a notification day the notification mail goes out at each of them.
- A bounce that reports several failed recipients counts every one of them in the alerts, the message details and the downloads.
- The DNS check verifies whether mail can be delivered to the reported name (its mail servers and their IPv4 and IPv6 addresses), whatever record type the notice names and however old the notice is. DNS lookup results are reused for 12 hours.
- A name lookup failure that could not be verified yet is shown as the new alert type "Name lookup failure (not verified)" instead of a delivery delay, and is checked again at each grouping while the DNS check is on.
- A delay notice whose reason is a sending-side problem (authentication, sender, size, content or server configuration) is filed under that problem.
- Name lookup failures reported by Exim, qmail, Microsoft Exchange and Microsoft 365, Gmail, and more Postfix and Sendmail wordings are recognized.
- The AI analysis receives every pattern of an alert, not only the first ones.
- The alert details show how many messages the alert has and link to the rest of them in the mail list.

### Fixed

- Work is no longer cut off by fixed time, size or count limits: analyses, DNS checks, mail fetching and sending, probe checks, downloads and web requests run to the end while the other side keeps responding, and the caps on notes, progress lines and probe cleanups are gone.
- The DNS check sends one query at a time instead of many at once, and a resolver that fails while others answer no longer makes a name look broken.
- Stored lookups of the sending sources keep standing in while a new lookup fails, so bounces of mail not sent from the sending sources are still recognized after 14 days of failed lookups.
- An analysis asked for by hand that stops at the agent's usage limit runs after the limit, also for an excluded alert.
- A failed lookup of a mail server name, rather than of the recipient domain, is no longer filed as a missing recipient domain.
- Bounces whose returned message has very many or very long Received headers are judged instead of being left as genuine bounces.

## [0.3.0] - 2026-10-16

### Added

- Bounces that report a failed name lookup are checked by looking the name up again, and are classified as a missing recipient domain, a DNS failure on the receiving side or a DNS failure of the sending side's mail server (new alert type). The check can be turned off or pointed at other resolvers in General settings.
- Sending sources can be registered: the SMTP server of each mailbox, and sending source addresses and host names in General settings.
- Fake delivery failure notices are classified as junk, including bounces of mail that was not sent from the sending sources.
- Delivery probe: mailboxes can send probe mails through their SMTP server at set times, and MailCare checks their arrival in a probe mailbox and reports delays and failures on the screen and by mail.

### Changed

- After upgrading, or after changing the sending sources or the DNS check, run "Reclassify" on the Tools page. Older bounces can become junk, which cleanup deletes from the IMAP server after the server retention: review the Junk tab first, or set the server retention to 0 until then. Alerts left without messages are removed.
- Notifications can be turned on only with an SMTP host and a sender address, and an SMTP username needs a password. The test email uses the SMTP settings as entered, and "Send now" works while notifications are off.
- "Analyze" and the tools are available only when the job can do something, and scheduled syncs and notifications that could do nothing are no longer queued. "Cleanup" is available without mailboxes.
- Command-line tools must run as the OS user of the service, with the same data directory, to reach the running server.
- Simpler, consistent wording on the screens and in notification emails. Alerts that come back for review are labelled "Resolved (re-check)" and "Ignored (re-check)"; renamed labels include "Mails" to "Messages", "Workers" to "Concurrent jobs", "Reindex" to "Rebuild index" and the mailbox retention to "Server retention (days)".

### Fixed

- The local control interface used by the command-line tools can no longer be reached through a reverse proxy on the same host.
- Settings saved by several administrators at the same time, or changed from the command line, can no longer end in an invalid combination.
- Starting the server and command-line tools at the same time right after an upgrade is now safe.
- Clearing the display name of a user restores the username instead of keeping the old name.

## [0.2.3] - 2026-09-30

### Added

- Marking an alert resolved asks what was done, and ignoring it asks why, each as a required choice. What was done can have optional details; a reason can have details only when it is "Other". Cancel saves nothing. This works from the alert list, the alert detail and the command line (`groups set-state --reason --note`).
- Each alert keeps a history of its state changes (when, who, the choice and the details), shown on the alert detail, in the PDF report and in the ZIP download. Reopening an alert clears its current choice and details; the history keeps them.
- New "Resolved (re)" and "Ignored (re)" tabs list resolved and ignored alerts that need a new decision. Their state stays as set until a new decision is made.
  - A resolved alert comes back when mail sent after the time the fix needs to take effect is still rejected. The days per category can be changed in the general settings (14 days for blacklists by default).
  - Resolved and ignored alerts of authentication, server configuration and DMARC problems also come back when a new kind of rejection arrives.
- Alerts that come back are analyzed again automatically. The AI judges whether the fix worked or the reason for ignoring still holds, from the analysis the decision was based on, what the user recorded and the notices received after the decision.
- The notification mail also lists the alerts that came back, with what was done or why they were ignored.

### Changed

- The alert and dashboard cards show the number of alerts to re-check next to the open ones. The dashboard also shows the fetched, target and junk mail counts, per mail address and in total, and no longer lists recent alert groups.
- In the mail list, the "Bounces" tab is now "Targets" and lists the same mails as the target count on the cards.

## [0.2.1] - 2026-09-30

### Added

- The overview card of an alert has a "Show report" button. It downloads a PDF report of the overview and the statistics, written in the language and time zone of the user who downloads it.
- The overview card of an alert has a "Download" button. It downloads a ZIP with the report contents as JSON, the index records of every source mail, the source mails themselves (original message, headers, text and HTML bodies), and a README describing the files and the JSON format, handy for AI agents and other tools.

### Fixed

- On Windows (and on minimal Linux containers), time zones such as Asia/Tokyo can now be chosen in the profile; they were rejected before, and notification mails used the server's time instead.

## [0.2.0] - 2026-09-29

### Added

- DMARC aggregate reports are now recognized. They count as target mails, and every record that failed DMARC appears as an alert, grouped by what it shows (SPF record missing, DKIM verification failed, sender not authenticated) with its sending IPs.
- Typical phishing (a sender name that impersonates the monitored domain or another organization, a link to an outside site that carries the monitored address, a forged From of the monitored domain) and mail flagged as spam are now recognized as junk. Junk is not an alert and is deleted from the server after the retention like the target mails.
- The Mails page shows the number of junk mails on each mail address card, and the mail list has a Junk tab.
- A Jobs page for administrators. "Active" lists the queued and running jobs, refreshes itself and lets a queued job be canceled; "History" lists the finished jobs, can be narrowed to done, error or canceled jobs and is paged 50 jobs at a time.
- Alerts can be marked as resolved or ignored, or reopened, directly from the alert list without opening their detail.

### Changed

- Automatic replies marked as sent by a machine (for example Exchange automatic replies) now count as target mails and are deleted from the server after the retention.
- Members can now change the state of alerts. Running an analysis remains for administrators.
- The Tools page, the job history (moved from the Tools page to the Jobs page) and the jobs on the dashboard are now for administrators only. Members still see on the dashboard which mail addresses are being synced.
- The alert list is paged 50 alerts at a time. The chosen order applies to all alerts, and the order and the page are kept in the URL.
- The page navigation of the lists (mails, alerts, job history) appears above the list as well as below it, uses icon buttons and can jump to the first and the last page.
- The "Excluded (recipient-side problems)" list of the Alerts page is now labeled "Excluded".
- Secondary buttons such as "Ignore" have a visible border and background, so they are easier to recognize as buttons.
- The alert list and the notification mail no longer show a recipient count for alerts without recipients, such as DMARC alerts.
- Mails fetched before this update, except delivery failures and delays, are classified again by the next sync or "Group only" run. Run "Reclassify" on the Tools page to apply the new rules to all mails.

### Fixed

- Mails delivered more than once with the same Message-ID (for example a report its sender sent again) are now each fetched and shown, instead of all but one being skipped. Run "Sync all" or "Fetch only" on the Tools page with the fetch period "all time" to fetch the ones skipped before.

## [0.1.3] - 2026-09-29

### Added

- The mail address cards of the Mails page now show the number of target mails (the bounces and other notices MailCare handles) next to the number of fetched mails.

## [0.1.2] - 2026-09-29

### Added

- The time of the daily cleanup can now be set in the general settings.
- "Sync all" and "Fetch only" on the Tools page can now fetch from all time, checking every mail in the folder regardless of the fetch days. The command line offers the same with `--all-time`.
- The mail address list now has a domain column.

### Changed

- The daily cleanup now runs at 2:00 by default instead of right after midnight.
- The server-side cleanup now deletes every bounce, delay notice, auto-reply and other mail server notice past the retention of its mail address, whatever the state of its alert. Mails recognized only by their subject or sender name, automatically generated messages and ordinary mail stay on the server.
- The retention of a mail address can no longer be set above the mail retention of the general settings.
- The mail address list is now sorted by domain, then by address.
- "Server retention" of a mail address is now labeled "Retention".
- New mail addresses now look back 10 days (instead of 30) on regular syncs.
- After the update, mails recognized as auto-replies are counted as awaiting grouping until the next sync.

### Fixed

- Mails could stay on the IMAP server for good when the retention of a mail address was longer than the mail retention.

## [0.1.1] - 2026-09-29

### Added

- Users who have an email address set can now log in with that email address instead of their username.

### Changed

- An email address can no longer be set on more than one user.
- Error messages in the web interface are now always shown in the user's language. Some of them used to appear in English.

### Fixed

- An invalid time zone was reported as a wrong time format.

## [0.1.0] - 2026-09-17

Initial release.
