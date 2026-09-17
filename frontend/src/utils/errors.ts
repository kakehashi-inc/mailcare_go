import type { TFunction } from 'i18next';
import { ApiError } from '../api/client';

/**
 * Turns any thrown value into a message for the user. Server validation
 * messages (400) are shown as-is because they are written for people.
 */
/**
 * Known user-facing messages of the backend, matched case-insensitively
 * against the response "error" text and translated. Unknown text is shown
 * as-is.
 */
const SERVER_MESSAGES: { match: RegExp; key: string }[] = [
    {
        match: /current password is wrong|current password (is )?incorrect|wrong current password/i,
        key: 'serverError.currentPasswordWrong',
    },
    { match: /username already exists|username (is )?taken|duplicate username/i, key: 'serverError.usernameExists' },
    {
        match: /address already exists|mailbox already exists|duplicate (mailbox|address)/i,
        key: 'serverError.mailboxExists',
    },
    {
        match: /invalid credentials|invalid username or password|login failed/i,
        key: 'serverError.invalidCredentials',
    },
    { match: /^forbidden$/i, key: 'error.forbidden' },
    { match: /^not found$/i, key: 'error.notFound' },
    { match: /^unauthorized$/i, key: 'error.unauthorized' },
    { match: /last admin(istrator)?/i, key: 'serverError.lastAdmin' },
    { match: /cannot delete (yourself|own account|self)/i, key: 'serverError.deleteSelf' },
    { match: /job is not queued|not queued|already (started|running|finished)/i, key: 'serverError.jobNotQueued' },
    { match: /password (is )?too short|password must be at least/i, key: 'serverError.passwordTooShort' },
    { match: /invalid (check )?time|time format/i, key: 'serverError.invalidTime' },
    { match: /smtp_password is required when the connection settings change/i, key: 'notify.passwordRequiredOnChange' },
    {
        match: /imap_password is required when the connection settings change/i,
        key: 'mailbox.passwordRequiredOnChange',
    },
];

/** Translates a known backend message, or returns it unchanged. */
export function serverMessage(message: string, t: TFunction): string {
    const text = message.trim();
    for (const entry of SERVER_MESSAGES) {
        if (entry.match.test(text)) return t(entry.key as 'error.forbidden');
    }
    return message;
}

export function errorMessage(err: unknown, t: TFunction): string {
    if (err instanceof ApiError) {
        if (err.isNetwork) return t('error.network');
        if (err.isUnauthorized) return t('error.unauthorized');
        if (err.isForbidden) return t('error.forbidden');
        if (err.status === 404) return t('error.notFound');
        if (err.status === 503) return t('error.unavailable');
        if (err.status >= 500) return t('error.server', { status: err.status });
        return serverMessage(err.message, t);
    }
    if (err instanceof Error) return err.message;
    return t('error.unknown');
}
