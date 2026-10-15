// Shapes of the JSON exchanged with the server (see the design document,
// section 9.3). Field names are the server's snake_case names.

export type Role = 'admin' | 'user';
export type GroupState = 'open' | 'resolved' | 'ignored';
/** What was done for a resolved group (value.resolveAction), in the order the dialog offers them ("other" last). */
export const RESOLVE_ACTIONS = [
    'delisting',
    'dns_fixed',
    'server_fixed',
    'sender_changed',
    'content_changed',
    'volume_adjusted',
    'recipient_fixed',
    'recipient_asked',
    'other',
] as const;
export type ResolveAction = (typeof RESOLVE_ACTIONS)[number];
/** Why a group was ignored (value.ignoreReason), in the order the dialog offers them ("other" last). */
export const IGNORE_REASONS = [
    'temporary',
    'recipient_side',
    'input_error',
    'stopped_sending',
    'spoofing',
    'external_service',
    'false_positive',
    'low_impact',
    'test_mail',
    'other',
] as const;
export type IgnoreReason = (typeof IGNORE_REASONS)[number];
/** Maximum length of the details written with a state change (characters). */
export const MAX_STATE_NOTE_LENGTH = 2000;
export type Responsible = 'sender' | 'recipient' | 'domain' | 'unknown';
export type Severity = 'high' | 'medium' | 'low' | '';
/** How firmly the agent established the cause from the notices. */
export type Confidence = 'high' | 'medium' | 'low';
export type ImapSecurity = 'ssl' | 'starttls' | 'none';
export type SmtpSecurity = 'ssl' | 'starttls' | 'none';
export type JobKind =
    | 'sync'
    | 'fetch'
    | 'group'
    | 'analyze'
    | 'reindex'
    | 'reclassify'
    | 'cleanup'
    | 'notify'
    | 'probe_send'
    | 'probe_check';
/** Job kinds that the tools page offers (notify is started from the notification settings, the probe jobs from the probe screens). */
export type ToolKind = Exclude<JobKind, 'notify' | 'probe_send' | 'probe_check'>;
/** Tool job kinds, in the order the tools page lists them. */
export const JOB_KINDS: readonly ToolKind[] = ['sync', 'fetch', 'group', 'analyze', 'reindex', 'reclassify', 'cleanup'];
export type JobStatus = 'queued' | 'running' | 'done' | 'error' | 'canceled';
/** Fetch status of a mailbox, derived on the client from last_fetched_at / last_fetch_error. */
export type CheckStatus = '' | 'ok' | 'error';
export type ReportStatus = '' | 'running' | 'completed' | 'error';
/** Which groups a list request returns: actionable (default), excluded (recipient-side problems) or both. */
export type GroupScope = 'actionable' | 'excluded' | 'all';
/** Body part used for bounce detection ('' = neither part had content). */
export type BodySource = 'text' | 'html' | '';

/**
 * Group categories assigned by the grouping phase (Documents/メール判定仕様.md).
 * The first twelve are actionable by the sending side (the three dmarc_* ones
 * bundle failing records of DMARC aggregate reports); the last six describe
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
    'dns_resolver_failure',
    'unknown_failure',
    'dmarc_spf_missing',
    'dmarc_dkim_failed',
    'dmarc_not_authenticated',
    'user_unknown',
    'mailbox_full',
    'mailbox_disabled',
    'domain_not_found',
    'dns_failure_external',
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
    /** Mails MailCare handles as its targets: notices classified with certain evidence (deleted from the IMAP server or not). */
    target_messages: number;
    /** Junk mails (phishing, spam): deleted from the server like the targets, counted apart. */
    junk_messages: number;
    /** Counts of the actionable Alerts list only (excluded groups are not counted). */
    groups: GroupCounts;
}

/**
 * Groups per tab of Alerts: resolved and ignored count the groups not sent back for a re-check,
 * resolved_recheck and ignored_recheck the ones that were (every group is counted once).
 */
export interface GroupCounts {
    open: number;
    resolved: number;
    resolved_recheck: number;
    ignored: number;
    ignored_recheck: number;
}

export interface MailboxDTO {
    id: number;
    address: string;
    display_name: string;
    /** The connection settings and fetch ranges are filled for administrators only; other users get "" / 0. */
    imap_host: string;
    imap_port: number;
    imap_security: ImapSecurity;
    imap_username: string;
    folder: string;
    enabled: boolean;
    initial_days: number;
    recent_days: number;
    /** Days (from the mail's date) a mail server notice classified with certain evidence (not by subject or sender name alone, not an auto-generated message) stays on the IMAP server, at most mail_keep_days; 0 = never deleted there. */
    server_keep_days: number;
    /** Host name of the SMTP server that sends the mail of this address (one of the own sending servers); "" = not set. Blanked ("") for non-administrators, like the IMAP connection. */
    smtp_host: string;
    /** The rest of the SMTP connection of smtp_host, through which the probe mails are sent. Blanked for non-administrators. */
    smtp_port: number;
    smtp_security: SmtpSecurity | '';
    smtp_username: string;
    /** The password itself is never returned; only whether one is stored. */
    smtp_password_set: boolean;
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
    /** Omitted: the default (60) for a new mailbox, unchanged on update. */
    server_keep_days?: number;
    /** SMTP server (sending) host name; "" clears it. Omitted: not set for a new mailbox, unchanged on update. Needs no password. */
    smtp_host?: string;
    /** The rest of the SMTP connection; used only with smtp_host ("" clears them all). */
    smtp_port?: number;
    smtp_security?: SmtpSecurity;
    /** Empty = no authentication. */
    smtp_username?: string;
    /** Empty keeps the stored one, but only while host, port, security and username stay the same. */
    smtp_password?: string;
}

export interface GroupDTO {
    group_key: string;
    /** The UI builds the headline from category, unit_value and authority (utils/category.ts). */
    category: BounceCategory | string;
    /** What the administrator acts on: an IP, a sender address, a domain, ... (the From domain for the dmarc_* groups). */
    unit_value: string;
    /** Who decides: a block list provider, the recipient domain, ... (empty for excluded groups and the dmarc_* groups). */
    authority: string;
    /** False for recipient-side problems, which are not analyzed automatically. */
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
    /** Code chosen with the current state (a ResolveAction or an IgnoreReason); null while open or when none was recorded. */
    state_reason: string | null;
    /** Details written with the current state; null when none. */
    state_note: string | null;
    /** A resolved or ignored group sent back for a new decision (the "(re)" tabs). */
    needs_recheck: boolean;
    needs_analysis: boolean;
    report_summary: string;
    report_severity: Severity | string;
    /** Confidence of the latest completed report; null when none was given. */
    report_confidence: Confidence | null;
    report_status: ReportStatus;
    /** The latest analysis failed for good (not a usage limit or a cancellation): not analyzed again until a new pattern arrives. */
    report_unanalyzable: boolean;
}

export interface ReportDTO {
    id: number;
    group_key: string;
    provider: string;
    status: string;
    summary: string;
    responsible: string;
    severity: string;
    /** How firmly the agent established the cause; null when it gave none. */
    confidence: Confidence | null;
    report_markdown: string;
    error_message: string;
    message_count: number;
    /** What the CLI reported about the run; null when it did not tell. */
    model: string | null;
    reasoning_effort: string | null;
    tokens_used: number | null;
    command_count: number | null;
    /** A failed run that settled its group (not a usage limit or a cancellation). */
    unanalyzable: boolean;
    /** A run refused by the agent's usage limit. */
    usage_limited: boolean;
    started_at: string | null;
    finished_at: string | null;
    created_at: string;
}

export interface MessageDTO {
    id: number;
    message_key: string;
    uid: number;
    /** IMAP UIDVALIDITY of the folder the uid belongs to. */
    uidvalidity: number;
    folder: string;
    message_id: string;
    subject: string;
    from_address: string;
    from_name: string;
    to_address: string;
    to_name: string;
    /** Date header as RFC 3339; the server falls back to the receive time, so it is never null. */
    date: string;
    received_at: string | null;
    size: number;
    /** Number of text body sections with content, in MIME order. */
    text_count: number;
    /** Number of HTML body sections with content. */
    html_count: number;
    /** Which body the bounce detection read: text, html, or none. */
    body_source: BodySource;
    /** False while the mail is still waiting for the grouping phase. */
    classified: boolean;
    is_bounce: boolean;
    bounce_kind: string;
    /** Name of the detection rule that matched; empty when no rule matched. */
    rule: string;
    group_key: string;
    fetched_at: string;
    /** When MailCare deleted the mail from the IMAP server (server retention); null while it is there. */
    server_deleted_at: string | null;
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
    kind: JobKind;
    mailbox_id: number | null;
    mailbox_address: string;
    /** True when the job's mailbox was deleted since (mailbox_id is null then, like an expansion job's). */
    mailbox_deleted: boolean;
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

/** Status filter of the job history: every finished job, or one finished status (done, error, canceled). */
export const JOB_HISTORY_FILTERS = ['all', 'done', 'error', 'canceled'] as const;
export type JobHistoryFilter = (typeof JOB_HISTORY_FILTERS)[number];

/** GET /api/v1/jobs?state=finished */
export interface JobHistoryParams {
    status?: JobHistoryFilter;
    page?: number;
    per_page?: number;
}
export interface JobHistoryResponse {
    jobs: JobDTO[];
    total: number;
    page: number;
    per_page: number;
    /** Finished jobs per status filter. */
    counts: Record<JobHistoryFilter, number>;
}

export interface JobInput {
    kind: JobKind;
    /** null (or omitted) means every mail address: the server queues one child job per address. */
    mailbox_id?: number | null;
    /** analyze: '' (groups awaiting analysis), '*' (every group) or a group key; sync / fetch: '' (fetch window) or '*' (all time). */
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
    /**
     * The counters of the Alerts and Mails cards summed over the mailboxes: open_groups and recheck_groups count
     * the actionable Alerts list; unclassified is the number of mails waiting to be grouped.
     */
    totals: {
        mailboxes: number;
        open_groups: number;
        recheck_groups: number;
        messages: number;
        target_messages: number;
        junk_messages: number;
        unclassified: number;
    };
    /** Mailboxes a sync, fetch, group, reindex, reclassify or cleanup job is queued or running for (every user). */
    busy_mailbox_ids: number[];
    /** Filled for administrators only (empty for members). */
    active_jobs: JobDTO[];
    recent_jobs: JobDTO[];
    next_check_at: string | null;
    check_times: string[];
    agent: { provider: string; enabled: boolean; available: boolean };
    probe: ProbeSummary;
}

export interface ProviderStatus {
    name: string;
    label: string;
    available: boolean;
    /** Option of the CLI that names the model ("--model"); empty when the provider cannot select a model. */
    model_option: string;
    /** Model names the CLI is known to accept, for reference only (may be empty). */
    models: string[];
    /** Option of the CLI that carries the reasoning level; empty when the provider cannot set one. */
    reasoning_option: string;
    /** Reasoning levels each known model accepts (may be empty when unknown). */
    reasoning_levels: Record<string, string[]>;
}

/** GET /api/v1/settings */
export interface SettingsDTO {
    check_times: string[];
    agent_provider: string;
    /** Model passed to the agent CLI; empty = the CLI's own default. */
    agent_model: string;
    /** Reasoning level passed to the agent CLI; empty = the CLI's own setting. */
    agent_reasoning_effort: string;
    agent_enabled: boolean;
    /** Days the workspace directory of an analysis run is kept (1-365). */
    agent_keep_days: number;
    /** Days a fetched mail is kept, counted from its date (1-3650). */
    mail_keep_days: number;
    /** Local time (HH:MM) of the daily cleanup. */
    cleanup_time: string;
    /**
     * Own sending servers, normalized: IPv4 / IPv6 addresses and CIDRs, and host names. Together with the SMTP
     * host of each mailbox (own_sending_mailbox_hosts) they tell whether a bounce is about mail we sent and
     * whether a notice about a failed DNS lookup came from our own mail server, judged per mail address
     * (Documents/メール判定仕様.md 2.6, 4.4). Present for administrators only, like every own_sending_* field and
     * dns_check_resolvers.
     */
    own_sending_ipv4?: string[];
    own_sending_ipv6?: string[];
    own_sending_hosts?: string[];
    /** Read-only: the distinct SMTP hosts of the mailboxes in mailbox order (set on each mailbox, not here). */
    own_sending_mailbox_hosts?: string[];
    /**
     * Read-only: the addresses of the mailboxes (in mailbox order) without an SMTP host while own_sending_hosts is
     * empty; notices about mail not sent from our servers are not judged for them. [] while own_sending_hosts is set.
     */
    own_sending_mailboxes_without_host?: string[];
    /** Whether a DNS lookup failure in a notice is verified by looking the name up again. */
    dns_check_enabled: boolean;
    /** IP addresses of the resolvers asked (kept but not used while dns_check_system_resolver_only is on). */
    dns_check_resolvers?: string[];
    /** Ask only the OS resolver instead of dns_check_resolvers. */
    dns_check_system_resolver_only: boolean;
    /** Re-check days of every group category (1-365). */
    recheck_days: Record<string, number>;
    providers: ProviderStatus[];
    /** Jobs executed at the same time (1-16). Present for administrators only. */
    workers?: number;
    /** Server internals, present for administrators only. */
    web_listen?: string;
    web_port?: number;
    data_dir?: string;
    /** IANA zone the scheduler interprets check_times, cleanup_time and notify_time in. */
    server_timezone: string;
}

export interface SettingsInput {
    check_times?: string[];
    agent_provider?: string;
    /** Empty restores the CLI default. */
    agent_model?: string;
    /** Empty restores the CLI's own setting. */
    agent_reasoning_effort?: string;
    agent_enabled?: boolean;
    agent_keep_days?: number;
    mail_keep_days?: number;
    cleanup_time?: string;
    /** Each list replaces the saved one (one entry per element; blank entries are ignored). */
    own_sending_ipv4?: string[];
    own_sending_ipv6?: string[];
    own_sending_hosts?: string[];
    dns_check_enabled?: boolean;
    /** An empty list is saved as empty (not the default); refused while the check needs a resolver. */
    dns_check_resolvers?: string[];
    dns_check_system_resolver_only?: boolean;
    /** Re-check days of the categories named (the others keep theirs). */
    recheck_days?: Record<string, number>;
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
/** Orders of the group list (server side): last seen oldest first, most messages first, severity; omitted = newest first. */
export type GroupSort = 'last_seen_asc' | 'count_desc' | 'severity';

export interface GroupListResponse {
    groups: GroupDTO[];
    /** Groups matching the filters (every page). */
    total: number;
    page: number;
    per_page: number;
    /** Per-tab counts within the requested scope (absent for the excluded scope, which is not counted). */
    counts?: GroupCounts;
}

/** One entry of the state history of a group. */
export interface StateChangeDTO {
    state: GroupState;
    /** Code chosen; null for open and for entries kept from before the history was recorded. */
    reason: string | null;
    note: string | null;
    /** Username; empty for the CLI and for entries kept from before the history was recorded. */
    changed_by: string;
    changed_at: string;
}

/** GET /api/v1/mailboxes/{id}/groups/{key} */
export interface GroupDetailResponse {
    group: GroupDTO;
    stats: { recipients: string[]; remote_ips: string[]; remote_mtas: string[] };
    report: ReportDTO | null;
    reports: ReportDTO[];
    messages: MessageDTO[];
    /** State history, newest first. */
    history: StateChangeDTO[];
}

/** GET /api/v1/mailboxes/{id}/messages */
/** Mail kind switch of the mail list: all, targets (notices classified with certain evidence), junk (phishing, spam), everything else. */
export type MessageKind = 'all' | 'target' | 'junk' | 'other';

export interface MessageListResponse {
    messages: MessageDTO[];
    total: number;
    page: number;
    per_page: number;
    /** Messages per kind for the same search and group filter. */
    counts: Record<MessageKind, number>;
}

/** GET /api/v1/mailboxes/{id}/messages/{key} */
export interface MessageDetailResponse {
    message: MessageDTO;
    bounce: BounceDTO | null;
    /** Text body sections in MIME order (one entry per section with content). */
    text_sections: string[];
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

// --- Delivery probe ---

/**
 * Status of a probe mail: pending (sent, not overdue yet), delayed / failure (not arrived for the delay / failure
 * threshold), send_failed (the SMTP server did not accept it), arrived (in time), recovered (arrived after the delay
 * threshold), resolved (closed by hand before it arrived).
 */
export type ProbeStatus = 'pending' | 'delayed' | 'failure' | 'send_failed' | 'arrived' | 'recovered' | 'resolved';
/** The worst level a probe mail reached ("" = none). */
export type ProbeLevel = '' | 'delayed' | 'failure';
/** Filters of the probe mail list: still open, every problem (open or closed), every mail. */
export const PROBE_FILTERS = ['open', 'issues', 'all'] as const;
export type ProbeFilter = (typeof PROBE_FILTERS)[number];
/** Why a probe mail was resolved by hand (value.probeResolveReason), in the order the dialog offers them ("other" last). */
export const PROBE_RESOLVE_REASONS = [
    'server_recovered',
    'arrived_elsewhere',
    'maintenance',
    'config_error',
    'probe_server_issue',
    'other',
] as const;
export type ProbeResolveReason = (typeof PROBE_RESOLVE_REASONS)[number];

export interface ProbeMailDTO {
    id: number;
    mailbox_id: number;
    address: string;
    token: string;
    /** "YYYY-MM-DD HH:MM" of the send time (server time, as in the subject); for a mail sent by hand the time it was sent. */
    slot: string;
    /** Sent by hand (not at a send time). */
    manual: boolean;
    subject: string;
    sent_at: string;
    status: ProbeStatus;
    level: ProbeLevel;
    /** Arrival minus sending once arrived, else the time without arrival at the latest check. */
    delay_seconds: number;
    arrived_at: string | null;
    last_checked_at: string | null;
    error_message: string;
    notified_at: string | null;
    resolved_at: string | null;
    resolved_by: string;
    resolve_reason: ProbeResolveReason | '';
    resolve_note: string;
}

export interface ProbeCounts {
    pending: number;
    delayed: number;
    failure: number;
    send_failed: number;
    /** Arrived late within the last recent_days. */
    recovered: number;
    /** Resolved by hand within the last recent_days. */
    resolved: number;
}

export interface ProbeMailboxState {
    mailbox_id: number;
    address: string;
    display_name: string;
    /** Enabled and has a probe SMTP server. */
    sending: boolean;
    latest: ProbeMailDTO | null;
}

/** State of the delivery probe (dashboard and probe screen). */
export interface ProbeSummary {
    enabled: boolean;
    configured: boolean;
    send_times: string[];
    delay_minutes: number;
    failure_minutes: number;
    next_send_at: string | null;
    next_check_at: string | null;
    last_send_at: string | null;
    last_check_at: string | null;
    /** The error of the latest check (only "error" for non-administrators). */
    last_check_error: string;
    counts: ProbeCounts;
    recent_days: number;
    mailboxes: ProbeMailboxState[];
}

/** GET /api/v1/probes */
export interface ProbeListResponse {
    summary: ProbeSummary;
    probes: ProbeMailDTO[];
    total: number;
    page: number;
    per_page: number;
    filter: ProbeFilter;
}

/** GET /api/v1/settings/probe */
export interface ProbeSettingsDTO {
    enabled: boolean;
    /** The recipient address of the probe mails (the mailbox of the probe server). */
    address: string;
    imap_host: string;
    imap_port: number;
    imap_security: ImapSecurity;
    imap_username: string;
    imap_password_set: boolean;
    imap_folder: string;
    /** HH:MM, server local time. */
    send_times: string[];
    delay_minutes: number;
    failure_minutes: number;
    /** Enabled mail addresses with a probe SMTP server. */
    mailboxes: number;
}

/** PUT /api/v1/settings/probe: only the fields present are changed; an empty imap_password keeps the stored one. */
export interface ProbeSettingsInput {
    enabled?: boolean;
    address?: string;
    imap_host?: string;
    imap_port?: number;
    imap_security?: ImapSecurity;
    imap_username?: string;
    imap_password?: string;
    imap_folder?: string;
    send_times?: string[];
    delay_minutes?: number;
    failure_minutes?: number;
}
