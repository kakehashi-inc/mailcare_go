# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
