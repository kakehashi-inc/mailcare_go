// Tunable frontend settings collected in one place so they are easy to find.

// --- Language ---

// Languages the UI ships translations for (see src/i18n/). The first entry is
// the language before login and the fallback for an unknown users.language.
export const SUPPORTED_LANGS = ['ja', 'en'] as const;
export type Lang = (typeof SUPPORTED_LANGS)[number];
export const DEFAULT_LANG: Lang = SUPPORTED_LANGS[0];

// --- Networking ---

// Base path of the JSON API and of the login/logout endpoints.
export const API_BASE = '/api/v1';
export const WEB_BASE = '/web';

// Delay before the single retry after a 503 (server still starting up).
export const RETRY_503_DELAY_MS = 1500;

// --- Polling ---

// Interval for refreshing job status while a job is queued or running.
export const JOB_POLL_INTERVAL_MS = 3000;

// Interval for refreshing the dashboard in the background.
export const DASHBOARD_REFRESH_MS = 30000;

// --- UI ---

// How long a toast stays on screen.
export const TOAST_DURATION_MS = 5000;

// Page size of the raw mail list.
export const MAIL_PAGE_SIZE = 50;

// Page size of the alert (group) list.
export const GROUP_PAGE_SIZE = 50;

// Page size of the job history (jobs page).
export const JOB_PAGE_SIZE = 50;

// Tailwind "md" / "lg" breakpoints, used by useMediaQuery.
export const BREAKPOINT_MD = 768;
export const BREAKPOINT_LG = 1024;

// Default values of a new mailbox form (mirrors the server defaults).
export const DEFAULT_IMAP_PORT_SSL = 993;
export const DEFAULT_IMAP_PORT_PLAIN = 143;
export const DEFAULT_INITIAL_DAYS = 90;
export const DEFAULT_RECENT_DAYS = 10;
export const DEFAULT_SERVER_KEEP_DAYS = 60;
export const MAX_SERVER_KEEP_DAYS = 3650;
/** Upper bound of initial_days / recent_days (days to look back when fetching). */
export const MAX_FETCH_DAYS = 3650;
export const DEFAULT_FOLDER = 'INBOX';

// Default values of the notification (SMTP) settings form (mirrors the server defaults).
export const DEFAULT_SMTP_PORT = 587;
export const DEFAULT_NOTIFY_TIME = '09:00';
// Notification interval choices: every day ... every 7 days.
export const NOTIFY_INTERVAL_MIN_DAYS = 1;
export const NOTIFY_INTERVAL_MAX_DAYS = 7;

// Default values of the delivery probe settings form (mirrors the server defaults).
export const DEFAULT_PROBE_SEND_TIMES = ['07:00', '11:00', '15:00', '19:00'];
export const DEFAULT_PROBE_DELAY_MINUTES = 15;
export const DEFAULT_PROBE_FAILURE_MINUTES = 60;
/** Bounds of the two probe thresholds (minutes). */
export const MIN_PROBE_MINUTES = 1;
export const MAX_PROBE_MINUTES = 7 * 24 * 60;
/** At most this many probe send times a day, at least this many minutes apart (the server checks the same). */
export const MAX_PROBE_SEND_TIMES = 24;
export const MIN_PROBE_SEND_GAP_MINUTES = 15;
/** The default identifier of the probe mails and its rule (the server checks the same). */
export const DEFAULT_PROBE_SUBJECT_TAG = 'MC';
export const PROBE_SUBJECT_TAG_RE = /^[A-Za-z0-9._-]{1,16}$/;
/** Maximum length of the details written when a probe mail is resolved by hand (characters). */
export const MAX_PROBE_NOTE_LENGTH = 2000;
// Page size of the probe mail list.
export const PROBE_PAGE_SIZE = 50;

// Minimum password length enforced client-side (the server validates too).
export const MIN_PASSWORD_LENGTH = 8;
