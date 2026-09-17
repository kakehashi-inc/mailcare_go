// Shapes of the JSON exchanged with the server (see Documents/システム設計書.md
// section 9.3). Field names are the server's snake_case names.

export type Role = 'admin' | 'user';
export type GroupState = 'open' | 'resolved' | 'ignored';
export type Responsible = 'sender' | 'recipient' | 'domain' | 'unknown';
export type Severity = 'high' | 'medium' | 'low' | '';
export type ImapSecurity = 'ssl' | 'starttls' | 'none';
export type JobKind = 'check' | 'reindex' | 'reclassify' | 'analyze';
export type JobStatus = 'queued' | 'running' | 'done' | 'error' | 'canceled';
export type CheckStatus = '' | 'ok' | 'error';
export type ReportStatus = '' | 'running' | 'completed' | 'error';

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
    role: Role;
    created_at: string;
    last_login_at: string | null;
}

/** GET /api/v1/me */
export interface Me {
    user: UserDTO;
    version: string;
}

export interface TokenDTO {
    id: number;
    user_id: number;
    username: string;
    identifier: string;
    name: string;
    expires_at: string | null;
    created_at: string;
    last_used_at: string | null;
}

export interface MailboxStats {
    messages: number;
    bounces: number;
    groups: { open: number; resolved: number; ignored: number };
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
    last_checked_at: string | null;
    last_check_status: CheckStatus;
    last_check_error: string;
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
    title: string;
    bounce_kind: string;
    recipient_domain: string;
    status_code: string;
    smtp_code: string;
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
    date: string | null;
    received_at: string | null;
    size: number;
    has_text: boolean;
    has_html: boolean;
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
    mailbox_id?: number;
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
    totals: { mailboxes: number; open_groups: number; bounces: number; messages: number };
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
    web_listen: string;
    web_port: number;
    data_dir: string;
}

export interface SettingsInput {
    check_times?: string[];
    agent_provider?: string;
    agent_enabled?: boolean;
}

/** GET /api/v1/mailboxes/{id}/groups */
export interface GroupListResponse {
    groups: GroupDTO[];
    counts: { open: number; resolved: number; ignored: number };
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

export interface LoginPasswordInput {
    username: string;
    password: string;
}

export interface LoginTokenInput {
    token: string;
}

export type LoginInput = LoginPasswordInput | LoginTokenInput;

export interface LoginResponse {
    ok: boolean;
    user: UserDTO;
}

export interface SetupInput {
    username: string;
    display_name: string;
    password: string;
}

export interface UserInput {
    username: string;
    display_name: string;
    password: string;
    role: Role;
}

export interface UserUpdateInput {
    display_name: string;
    role: Role;
}

export interface TokenInput {
    user_id?: number;
    name: string;
    /** RFC 3339 timestamp; omitted for a token that never expires. */
    expires?: string;
}

export interface TokenCreateResponse {
    token: TokenDTO;
    value: string;
}
