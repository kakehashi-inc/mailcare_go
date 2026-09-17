// Shapes of the JSON exchanged with the server (see Documents/システム設計書.md
// section 9.3). Field names are the server's snake_case names.

export type Role = 'admin' | 'user';
export type GroupState = 'open' | 'resolved' | 'ignored';
export type Responsible = 'sender' | 'recipient' | 'domain' | 'unknown';
export type Severity = 'high' | 'medium' | 'low' | '';
export type ImapSecurity = 'ssl' | 'starttls' | 'none';
export type SmtpSecurity = 'ssl' | 'starttls' | 'none';
export type JobKind = 'sync' | 'fetch' | 'group' | 'analyze' | 'reindex' | 'reclassify' | 'notify';
/** Job kinds that the tools page offers (notify is started from the notification settings). */
export type ToolKind = Exclude<JobKind, 'notify'>;
/** Tool job kinds, in the order the tools page lists them. */
export const JOB_KINDS: readonly ToolKind[] = ['sync', 'fetch', 'group', 'analyze', 'reindex', 'reclassify'];
/** Every job kind the server can report, for labelling job rows. */
export const ALL_JOB_KINDS: readonly JobKind[] = [...JOB_KINDS, 'notify'];
export type JobStatus = 'queued' | 'running' | 'done' | 'error' | 'canceled';
/** Fetch status of a mailbox, derived on the client from last_fetched_at / last_fetch_error. */
export type CheckStatus = '' | 'ok' | 'error';
export type ReportStatus = '' | 'running' | 'completed' | 'error';
/** Which groups a list request returns: actionable (default), excluded (recipient-side problems) or both. */
export type GroupScope = 'actionable' | 'excluded' | 'all';
/** Body part used for bounce detection ('' = neither part had content). */
export type BodySource = 'text' | 'html' | '';

/**
 * Bounce categories assigned by the grouping phase (design document 5.4).
 * The first eight are actionable by the sending side; the last five describe
 * recipient-side problems and are excluded from analysis.
 */
export const BOUNCE_CATEGORIES = [
    'ip_blocked',
    'rate_limited',
    'auth_failure',
    'sender_blocked',
    'content_rejected',
    'message_too_large',
    'server_config',
    'unknown_failure',
    'user_unknown',
    'mailbox_full',
    'mailbox_disabled',
    'domain_not_found',
    'delivery_delay',
] as const;
export type BounceCategory = (typeof BOUNCE_CATEGORIES)[number];

/** GET /api/v1/health */
export interface Health {
    status: string;
    version: string;
}

/** GET /api/v1/setup */
export interface SetupStatus {
    needs_setup: boolean;
}

export interface UserDTO {
    id: number;
    username: string;
    display_name: string;
    /** Notification mail address; empty when the user has none (then never a recipient). */
    email: string;
    /** UI language ('ja' | 'en'); applied after login. */
    language: string;
    /** IANA zone every timestamp is displayed in for this user (default Asia/Tokyo). */
    timezone: string;
    /** Color theme: auto (browser preference), light or dark. */
    theme: string;
    role: Role;
    created_at: string;
    last_login_at: string | null;
}

/** GET /api/v1/me */
export interface Me {
    user: UserDTO;
    version: string;
}

/** A login token reserved for the future API; tokens belong to no user. */
export interface TokenDTO {
    id: number;
    identifier: string;
    name: string;
    /** The token the server creates itself when none exists. */
    is_default: boolean;
    expires_at: string | null;
    created_at: string;
}

export interface MailboxStats {
    messages: number;
    bounces: number;
    /** Mails fetched but not grouped yet. */
    unclassified: number;
    /** Counts of actionable groups only. */
    groups: { open: number; resolved: number; ignored: number };
    /** Number of excluded (recipient-side) groups. */
    excluded_groups: number;
}

export interface MailboxDTO {
    id: number;
    address: string;
    display_name: string;
    imap_host: string;
    imap_port: number;
    imap_security: ImapSecurity;
    imap_username: string;
    folder: string;
    enabled: boolean;
    initial_days: number;
    recent_days: number;
    /** Null until the first fetch; the status is derived: null = never, error text = error, otherwise ok. */
    last_fetched_at: string | null;
    last_fetch_error: string;
    created_at: string;
    updated_at: string;
    stats?: MailboxStats;
}

export interface MailboxInput {
    id?: number;
    address: string;
    display_name?: string;
    imap_host: string;
    imap_port?: number;
    imap_security?: ImapSecurity;
    imap_username: string;
    imap_password?: string;
    folder?: string;
    enabled?: boolean;
    initial_days?: number;
    recent_days?: number;
}

export interface GroupDTO {
    group_key: string;
    /** The UI builds the headline from category, unit_value and authority (utils/category.ts). */
    category: BounceCategory | string;
    /** What the administrator acts on: an IP, a sender address, a domain, ... */
    unit_value: string;
    /** Who decides: a block list provider, the recipient domain, ... (empty for excluded groups). */
    authority: string;
    /** False for recipient-side problems, which are never analyzed. */
    actionable: boolean;
    recipient_domain: string;
    status_code: string;
    diagnostic_template: string;
    responsible: Responsible | string;
    message_count: number;
    recipient_count: number;
    remote_ip_count: number;
    first_seen: string | null;
    last_seen: string | null;
    state: GroupState;
    state_updated_at: string | null;
    needs_analysis: boolean;
    report_summary: string;
    report_severity: Severity | string;
    report_status: ReportStatus;
}

/** A group row on the dashboard, which spans mailboxes. */
export interface DashboardGroup extends GroupDTO {
    mailbox_id: number;
    mailbox_address: string;
}

export interface ReportDTO {
    id: number;
    group_key: string;
    provider: string;
    status: string;
    summary: string;
    responsible: string;
    severity: string;
    report_markdown: string;
    error_message: string;
    message_count: number;
    started_at: string | null;
    finished_at: string | null;
    created_at: string;
}

export interface MessageDTO {
    id: number;
    message_key: string;
    uid: number;
    folder: string;
    message_id: string;
    subject: string;
    from_address: string;
    from_name: string;
    to_address: string;
    to_name: string;
    date: string | null;
    received_at: string | null;
    size: number;
    /** True when the text part has real content. */
    has_text: boolean;
    has_html: boolean;
    /** Which body the bounce detection read: text, html, or none. */
    body_source: BodySource;
    is_bounce: boolean;
    bounce_kind: string;
    classify_reason: string;
    group_key: string;
    fetched_at: string;
}

export interface BounceDTO {
    original_recipient: string;
    recipient_domain: string;
    action: string;
    status_code: string;
    smtp_code: string;
    diagnostic: string;
    diagnostic_template: string;
    remote_mta: string;
    remote_ip: string;
    reporting_mta: string;
    original_message_id: string;
    original_subject: string;
    original_from: string;
    original_date: string | null;
    responsible: string;
}

export interface JobDTO {
    id: number;
    kind: JobKind | string;
    mailbox_id: number | null;
    mailbox_address: string;
    target: string;
    status: JobStatus;
    progress: string;
    result: string;
    error_message: string;
    requested_by: string;
    created_at: string;
    started_at: string | null;
    finished_at: string | null;
}

export interface JobInput {
    kind: JobKind;
    /** null (or omitted) means every mail address: the server queues one child job per address. */
    mailbox_id?: number | null;
    target?: string;
}

/** Single-object responses are wrapped by the server: {mailbox}, {user}, {group}, {token, value}. */
export interface MailboxResponse {
    mailbox: MailboxDTO;
}
export interface UserResponse {
    user: UserDTO;
}
export interface GroupResponse {
    group: GroupDTO;
}

/** Response of every job-submitting endpoint: created is false when an identical job was already queued or running. */
export interface JobSubmitResult {
    job: JobDTO;
    created: boolean;
}

export interface DashboardDTO {
    mailboxes: MailboxDTO[];
    /** open_groups counts actionable groups only; unclassified is the number of mails waiting to be grouped. */
    totals: { mailboxes: number; open_groups: number; bounces: number; messages: number; unclassified: number };
    recent_groups: DashboardGroup[];
    active_jobs: JobDTO[];
    recent_jobs: JobDTO[];
    next_check_at: string | null;
    check_times: string[];
    agent: { provider: string; enabled: boolean; available: boolean };
}

export interface ProviderStatus {
    name: string;
    label: string;
    available: boolean;
}

/** GET /api/v1/settings */
export interface SettingsDTO {
    check_times: string[];
    agent_provider: string;
    agent_enabled: boolean;
    providers: ProviderStatus[];
    /** Jobs executed at the same time (1-16). */
    workers: number;
    web_listen: string;
    web_port: number;
    data_dir: string;
    /** IANA zone the scheduler interprets check_times and notify_time in. */
    server_timezone: string;
}

export interface SettingsInput {
    check_times?: string[];
    agent_provider?: string;
    agent_enabled?: boolean;
    workers?: number;
}

/** A user as listed in the notification recipient picker (every user, with or without an address). */
export interface NotificationRecipient {
    id: number;
    username: string;
    display_name: string;
    email: string;
}

/** GET /api/v1/settings/notifications */
export interface NotificationSettingsDTO {
    smtp_host: string;
    smtp_port: number;
    smtp_security: SmtpSecurity;
    smtp_username: string;
    /** The password itself is never returned; only whether one is stored. */
    smtp_password_set: boolean;
    smtp_from: string;
    public_base_url: string;
    /** public_base_url, or the server's own address when it is empty. */
    effective_base_url: string;
    notify_enabled: boolean;
    /** HH:MM, server local time. */
    notify_time: string;
    /** 1 = every day ... 7 = every 7 days. */
    notify_interval_days: number;
    notify_user_ids: number[];
    last_sent_at: string | null;
    next_send_at: string | null;
    recipients: NotificationRecipient[];
}

/** PUT /api/v1/settings/notifications: only the fields present are changed; an empty smtp_password keeps the stored one. */
export interface NotificationSettingsInput {
    smtp_host?: string;
    smtp_port?: number;
    smtp_security?: SmtpSecurity;
    smtp_username?: string;
    smtp_password?: string;
    smtp_from?: string;
    public_base_url?: string;
    notify_enabled?: boolean;
    notify_time?: string;
    notify_interval_days?: number;
    notify_user_ids?: number[];
}

/** POST /api/v1/notifications/test: "to" defaults to the caller's own address on the server. */
export interface NotificationTestInput {
    to?: string;
}

/** GET /api/v1/mailboxes/{id}/groups */
export interface GroupListResponse {
    groups: GroupDTO[];
    /** Per-state counts within the requested scope. */
    counts: { open: number; resolved: number; ignored: number };
    /** Total number of excluded groups, regardless of scope. */
    excluded_count: number;
}

/** GET /api/v1/mailboxes/{id}/groups/{key} */
export interface GroupDetailResponse {
    group: GroupDTO;
    stats: { recipients: string[]; remote_ips: string[]; remote_mtas: string[] };
    report: ReportDTO | null;
    reports: ReportDTO[];
    messages: MessageDTO[];
}

/** GET /api/v1/mailboxes/{id}/messages */
export interface MessageListResponse {
    messages: MessageDTO[];
    total: number;
    page: number;
    per_page: number;
}

/** GET /api/v1/mailboxes/{id}/messages/{key} */
export interface MessageDetailResponse {
    message: MessageDTO;
    bounce: BounceDTO | null;
    text: string;
    has_html: boolean;
    /** The parsed-message JSON (message_id, subject, from, to, date, delivery_status, ...). */
    headers: Record<string, unknown>;
}

/** POST /web/login: remember asks for a long-lived session (the server decides the cookie lifetime). */
export interface LoginInput {
    username: string;
    password: string;
    remember: boolean;
}

export interface LoginResponse {
    ok: boolean;
    user: UserDTO;
}

/** POST /web/setup: the first administrator, with the same optional preferences as a user. */
export interface SetupInput {
    username: string;
    display_name: string;
    email?: string;
    language?: string;
    timezone?: string;
    theme?: string;
    password: string;
}

export interface UserInput {
    username: string;
    display_name: string;
    /** Notification mail address; empty for none. */
    email?: string;
    language?: string;
    /** IANA zone; the server defaults to Asia/Tokyo when omitted. */
    timezone?: string;
    theme?: string;
    password: string;
    role: Role;
}

export interface UserUpdateInput {
    display_name?: string;
    /** Empty string clears the address. */
    email?: string;
    language?: string;
    timezone?: string;
    theme?: string;
    role?: Role;
}

/** PUT /api/v1/me/profile: only the fields present change; an empty email clears it. */
export interface ProfileInput {
    display_name?: string;
    email?: string;
    language?: string;
    timezone?: string;
    theme?: string;
}

export interface TokenInput {
    name: string;
    /** Chosen identifier; the server generates one when omitted. */
    identifier?: string;
    /** RFC 3339 timestamp; omitted for a token that never expires. */
    expires?: string;
}

export interface TokenCreateResponse {
    token: TokenDTO;
    value: string;
}
