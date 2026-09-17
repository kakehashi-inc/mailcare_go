import { API_BASE, RETRY_503_DELAY_MS, WEB_BASE } from '../constants';
import type {
    BounceDTO,
    DashboardDTO,
    GroupDetailResponse,
    GroupDTO,
    GroupListResponse,
    GroupResponse,
    GroupScope,
    GroupState,
    Health,
    JobDTO,
    JobInput,
    JobSubmitResult,
    LoginInput,
    LoginResponse,
    MailboxDTO,
    MailboxInput,
    MailboxResponse,
    Me,
    MessageDetailResponse,
    MessageListResponse,
    SettingsDTO,
    SettingsInput,
    SetupInput,
    SetupStatus,
    TokenCreateResponse,
    TokenDTO,
    TokenInput,
    UserDTO,
    UserInput,
    UserResponse,
    UserUpdateInput,
} from '../types';

/** Raised for any non-2xx response, or with status 0 when the network failed. */
export class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
        super(message);
        this.name = 'ApiError';
        this.status = status;
    }
    get isUnauthorized(): boolean {
        return this.status === 401;
    }
    get isForbidden(): boolean {
        return this.status === 403;
    }
    get isNetwork(): boolean {
        return this.status === 0;
    }
}

/**
 * Called on every 401 unless the request opted out. The AuthProvider installs
 * a handler that clears the session and navigates to /login with the current
 * location as "next"; before it is installed we fall back to a hard redirect.
 */
let unauthorizedHandler: () => void = () => {
    const here = window.location.pathname + window.location.search;
    if (!here.startsWith('/login') && !here.startsWith('/setup')) {
        window.location.assign(`/login?next=${encodeURIComponent(here)}`);
    }
};

export function setUnauthorizedHandler(handler: () => void): void {
    unauthorizedHandler = handler;
}

interface RequestOptions {
    /** Do not trigger the unauthorized handler on 401 (used by /me probes and login). */
    silent401?: boolean;
}

function sleep(ms: number): Promise<void> {
    return new Promise(resolve => window.setTimeout(resolve, ms));
}

async function readError(res: Response): Promise<string> {
    let message = `HTTP ${res.status}`;
    try {
        const body = await res.json();
        if (body && typeof body.error === 'string') message = body.error;
    } catch {
        // The body is not JSON; keep the status text.
    }
    return message;
}

async function request<T>(url: string, init: RequestInit = {}, opts: RequestOptions = {}): Promise<T> {
    let res: Response;
    const doFetch = async (): Promise<Response> => {
        try {
            return await fetch(url, { credentials: 'same-origin', ...init });
        } catch {
            throw new ApiError(0, 'network');
        }
    };
    res = await doFetch();
    if (res.status === 503) {
        // The server is starting up (or restarting): retry exactly once.
        await sleep(RETRY_503_DELAY_MS);
        res = await doFetch();
    }
    if (res.status === 401 && !opts.silent401) {
        unauthorizedHandler();
    }
    if (!res.ok) {
        throw new ApiError(res.status, await readError(res));
    }
    if (res.status === 204 || res.headers.get('content-length') === '0') {
        return undefined as T;
    }
    const text = await res.text();
    if (!text) return undefined as T;
    return JSON.parse(text) as T;
}

function json(method: string, body?: unknown): RequestInit {
    return {
        method,
        headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
    };
}

function api(path: string): string {
    return `${API_BASE}${path}`;
}

function query(params: Record<string, string | number | boolean | undefined>): string {
    const sp = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
        if (v === undefined || v === '' || v === false) continue;
        sp.set(k, v === true ? '1' : String(v));
    }
    const s = sp.toString();
    return s ? `?${s}` : '';
}

// --- Public / auth ---

export function getHealth(): Promise<Health> {
    return request<Health>(api('/health'));
}

export function getSetupStatus(): Promise<SetupStatus> {
    return request<SetupStatus>(api('/setup'));
}

export function setup(input: SetupInput): Promise<LoginResponse> {
    return request<LoginResponse>(`${WEB_BASE}/setup`, json('POST', input), { silent401: true });
}

export function login(input: LoginInput): Promise<LoginResponse> {
    return request<LoginResponse>(`${WEB_BASE}/login`, json('POST', input), { silent401: true });
}

export function logout(): Promise<void> {
    return request<void>(`${WEB_BASE}/logout`, json('POST'), { silent401: true });
}

export function getMe(): Promise<Me> {
    return request<Me>(api('/me'), {}, { silent401: true });
}

export function changeMyPassword(current_password: string, new_password: string): Promise<void> {
    return request<void>(api('/me/password'), json('PUT', { current_password, new_password }));
}

// --- Dashboard ---

export function getDashboard(): Promise<DashboardDTO> {
    return request<DashboardDTO>(api('/dashboard'));
}

// --- Mailboxes ---

/** Mailbox list. Pass withStats to include per-mailbox counters (an extra query for the server). */
export async function listMailboxes(withStats = false): Promise<MailboxDTO[]> {
    const r = await request<{ mailboxes: MailboxDTO[] }>(api(`/mailboxes${query({ stats: withStats })}`));
    return r.mailboxes ?? [];
}

// Single-object endpoints wrap the DTO ({mailbox: ...}); the client unwraps it.
export async function createMailbox(input: MailboxInput): Promise<MailboxDTO> {
    const r = await request<MailboxResponse>(api('/mailboxes'), json('POST', input));
    return r.mailbox;
}

export async function getMailbox(id: number): Promise<MailboxDTO> {
    const r = await request<MailboxResponse>(api(`/mailboxes/${id}`));
    return r.mailbox;
}

export async function updateMailbox(id: number, input: MailboxInput): Promise<MailboxDTO> {
    const r = await request<MailboxResponse>(api(`/mailboxes/${id}`), json('PUT', input));
    return r.mailbox;
}

export function deleteMailbox(id: number, keepData: boolean): Promise<void> {
    return request<void>(api(`/mailboxes/${id}${query({ keep_data: keepData })}`), json('DELETE'));
}

export function testMailbox(input: MailboxInput): Promise<{ ok: boolean }> {
    return request<{ ok: boolean }>(api('/mailboxes/test'), json('POST', input));
}

/** Queues a sync job (fetch, group, analyze) for one mailbox. */
export function syncMailbox(id: number): Promise<JobSubmitResult> {
    return request<JobSubmitResult>(api(`/mailboxes/${id}/sync`), json('POST'));
}

// --- Groups (alerts) ---

export interface GroupListParams {
    /** Defaults to "actionable" on the server. */
    scope?: GroupScope;
    state?: GroupState | '';
    category?: string;
    responsible?: string;
    q?: string;
}

export function listGroups(mailboxId: number, params: GroupListParams = {}): Promise<GroupListResponse> {
    return request<GroupListResponse>(api(`/mailboxes/${mailboxId}/groups${query({ ...params })}`));
}

export function getGroup(mailboxId: number, key: string): Promise<GroupDetailResponse> {
    return request<GroupDetailResponse>(api(`/mailboxes/${mailboxId}/groups/${encodeURIComponent(key)}`));
}

/** Changes the group state and returns the updated group ({group: ...} unwrapped). */
export async function setGroupState(mailboxId: number, key: string, state: GroupState): Promise<GroupDTO> {
    const r = await request<GroupResponse>(
        api(`/mailboxes/${mailboxId}/groups/${encodeURIComponent(key)}/state`),
        json('PUT', { state })
    );
    return r.group;
}

export function analyzeGroup(mailboxId: number, key: string): Promise<JobSubmitResult> {
    return request<JobSubmitResult>(
        api(`/mailboxes/${mailboxId}/groups/${encodeURIComponent(key)}/analyze`),
        json('POST')
    );
}

// --- Messages (mails) ---

export interface MessageListParams {
    q?: string;
    only_bounce?: boolean;
    group?: string;
    page?: number;
    per_page?: number;
}

export function listMessages(mailboxId: number, params: MessageListParams = {}): Promise<MessageListResponse> {
    return request<MessageListResponse>(api(`/mailboxes/${mailboxId}/messages${query({ ...params })}`));
}

export function getMessage(mailboxId: number, key: string): Promise<MessageDetailResponse> {
    return request<MessageDetailResponse>(api(`/mailboxes/${mailboxId}/messages/${encodeURIComponent(key)}`));
}

/** URL of the sanitized HTML body, for a sandboxed iframe. */
export function messageHtmlUrl(mailboxId: number, key: string): string {
    return api(`/mailboxes/${mailboxId}/messages/${encodeURIComponent(key)}/html`);
}

/** URL of the raw .eml download. */
export function messageRawUrl(mailboxId: number, key: string): string {
    return api(`/mailboxes/${mailboxId}/messages/${encodeURIComponent(key)}/raw`);
}

// --- Jobs ---

export async function listJobs(limit?: number): Promise<JobDTO[]> {
    const r = await request<{ jobs: JobDTO[] }>(api(`/jobs${query({ limit })}`));
    return r.jobs ?? [];
}

export function createJob(input: JobInput): Promise<JobSubmitResult> {
    return request<JobSubmitResult>(api('/jobs'), json('POST', input));
}

export async function getJob(id: number): Promise<JobDTO> {
    const r = await request<{ job: JobDTO }>(api(`/jobs/${id}`));
    return r.job;
}

export function cancelJob(id: number): Promise<void> {
    return request<void>(api(`/jobs/${id}`), json('DELETE'));
}

// --- Settings ---

export function getSettings(): Promise<SettingsDTO> {
    return request<SettingsDTO>(api('/settings'));
}

export function updateSettings(input: SettingsInput): Promise<SettingsDTO> {
    return request<SettingsDTO>(api('/settings'), json('PUT', input));
}

// --- Users ---

export async function listUsers(): Promise<UserDTO[]> {
    const r = await request<{ users: UserDTO[] }>(api('/users'));
    return r.users ?? [];
}

export async function createUser(input: UserInput): Promise<UserDTO> {
    const r = await request<UserResponse>(api('/users'), json('POST', input));
    return r.user;
}

export async function updateUser(id: number, input: UserUpdateInput): Promise<UserDTO> {
    const r = await request<UserResponse>(api(`/users/${id}`), json('PUT', input));
    return r.user;
}

export function setUserPassword(id: number, new_password: string): Promise<void> {
    return request<void>(api(`/users/${id}/password`), json('PUT', { new_password }));
}

export function deleteUser(id: number): Promise<void> {
    return request<void>(api(`/users/${id}`), json('DELETE'));
}

// --- Tokens ---

export async function listTokens(userId?: number): Promise<TokenDTO[]> {
    const r = await request<{ tokens: TokenDTO[] }>(api(`/tokens${query({ user_id: userId })}`));
    return r.tokens ?? [];
}

export function createToken(input: TokenInput): Promise<TokenCreateResponse> {
    return request<TokenCreateResponse>(api('/tokens'), json('POST', input));
}

export function deleteToken(identifier: string): Promise<void> {
    return request<void>(api(`/tokens/${encodeURIComponent(identifier)}`), json('DELETE'));
}

export type { BounceDTO };
