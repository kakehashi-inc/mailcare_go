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

// Number of jobs shown on the tools page and the dashboard.
export const JOB_LIST_LIMIT = 30;

// Tailwind "md" / "lg" breakpoints, used by useMediaQuery.
export const BREAKPOINT_MD = 768;
export const BREAKPOINT_LG = 1024;

// Default values of a new mailbox form (mirrors the server defaults).
export const DEFAULT_IMAP_PORT_SSL = 993;
export const DEFAULT_IMAP_PORT_PLAIN = 143;
export const DEFAULT_INITIAL_DAYS = 90;
export const DEFAULT_RECENT_DAYS = 30;
export const DEFAULT_FOLDER = 'INBOX';

// Default values of the notification (SMTP) settings form (mirrors the server defaults).
export const DEFAULT_SMTP_PORT = 587;
export const DEFAULT_NOTIFY_TIME = '09:00';
// Notification interval choices: every day ... every 7 days.
export const NOTIFY_INTERVAL_MIN_DAYS = 1;
export const NOTIFY_INTERVAL_MAX_DAYS = 7;

// Minimum password length enforced client-side (the server validates too).
export const MIN_PASSWORD_LENGTH = 8;
