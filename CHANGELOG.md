# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Delivery probe: MailCare can check that mail actually gets through. At set times (07:00, 11:00, 15:00 and 19:00 by default) every mail address with a probe SMTP server sends a probe mail to a probe mailbox that MailCare reads over IMAP. The subject names the date and time of the send and the sending address, so a missing mail can be traced.
- The arrival of all probe mails is checked at once, a set delay threshold after the latest send (30 minutes by default). A mail not arrived by then is marked "Delayed"; one not arrived by the failure threshold (120 minutes by default) is a "Failure" and is mailed to the notification recipients. While mails are missing, the check repeats at the failure threshold; when the next send time comes, the new check also covers the older mails. The delay of every mail is measured from its own send time and kept up to date.
- A probe mail that arrives late is shown as "Recovered" and still shows that it was delayed or failed. Administrators can mark a missing probe mail resolved without waiting, choosing a reason.
- New "Delivery probe" screen under Alerts, a probe card and failure / delay notices on the dashboard, the latest probe status on each mail address card, and new settings: "Settings - Delivery probe" (probe mailbox, send times, thresholds) and a "Probe mail sending (SMTP)" section with a test button for each mail address. The command line has `probe send`, `probe check`, the `probe_*` settings and `--probe-smtp-*` options for `mailbox add` / `mailbox update`.
- Delivery-failure notices that report a failed name lookup ("Host not found, try again" and the like) are now checked: MailCare looks the name up itself. A recipient domain that really does not exist stays "Recipient domain not found"; a DNS failure on the receiving side becomes the new excluded type "DNS failure (receiving side or external)"; a failure of your own mail server's name resolution becomes the new alert type "DNS resolver failure (own server)". The AI cause analysis is given what the check found. Notices older than two days are not looked up again, and results kept from earlier checks keep the classification stable.
- The DNS check is on by default. In the general settings it can be switched off, pointed at other resolvers or limited to the operating system's resolver. Its queries leave the server as plain DNS to the resolvers you set (Cloudflare, Google and Quad9 by default) and, for notices about your own mail, to the name servers of the domain concerned; the README section "Outbound connections" explains what they reveal.
- Fake "mail delivery failed" notices are recognized as junk: those that carry links to other sites, those that fail the sender check, and notices about mail that was never sent from your own servers (someone used your address as the sender). Like other junk, they are deleted from the IMAP server after the retention of the address.
- Your own sending servers can be registered: each mail address has an "SMTP server (sending)" field, and the general settings take other sending servers (host names and IPv4 / IPv6 addresses). Their addresses are looked up automatically, and while the DNS check is on, the SPF records of your domains are read too. Like the IMAP connection settings, these are shown to administrators only.
- Notices about mail you did not send are recognized for an address only when a host name is known for it: its own SMTP server, or a host name in the general settings, which counts for every address. Register every route you send through: a bounce of mail sent through a route that is not registered is taken for junk unless the mail is signed (DKIM) with your domain. The general settings list the addresses that have no host name.

### Changed

- After upgrading, and after changing your sending servers or the DNS check settings, run "Reclassify" once (Tools screen, or `mailcare reclassify`) to apply them to the mail already fetched. Reclassify can turn older notices into junk, which the next cleanup deletes from the IMAP server once it is past the retention of its address: review the Junk tab first, or set the retention to 0 until you have. Alerts left without any mail are removed together with their state and history.

## [0.2.3] - 2026-09-30

### Added

- Marking an alert resolved asks what was done, and ignoring it asks why, each as a required choice. What was done can have optional details; for the reason, "Other" lets you write it. Cancel saves nothing. This works from the alert list, the alert detail and the command line (`groups set-state --reason --note`).
- Each alert keeps a history of its state changes (when, who, the choice and the details), shown on the alert detail, in the PDF report and in the ZIP download. Reopening an alert clears its current choice and details; the history keeps them.
- New "Resolved (re)" and "Ignored (re)" tabs list resolved and ignored alerts that need a new decision. Their state stays as you set it until you decide again.
  - A resolved alert comes back when mail sent after the time the fix needs to take effect is still rejected. The days per category can be changed in the general settings (14 days for blacklists by default).
  - Resolved and ignored alerts of authentication, server configuration and DMARC problems also come back when a new kind of rejection arrives.
- Alerts that come back are analyzed again automatically. The AI judges whether the fix worked or the reason for ignoring still holds, from the analysis the decision was based on, what you recorded and the notices received after the decision.
- The notification mail also lists the alerts that came back, with what was done or why they were ignored.

### Changed

- The alert and dashboard cards show the number of alerts to re-check next to the open ones. The dashboard also shows the fetched, target and junk mail counts, per mail address and in total, and no longer lists recent alert groups.
- In the mail list, the "Bounces" tab is now "Targets" and lists the same mails as the target count on the cards.

## [0.2.1] - 2026-09-30

### Added

- The overview card of an alert has a "Show report" button. It downloads a PDF report of the overview and the statistics, written in your language and time zone.
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
- Error messages in the web interface are now always shown in your language. Some of them used to appear in English.

### Fixed

- An invalid time zone was reported as a wrong time format.

## [0.1.0] - 2026-09-17

Initial release.
