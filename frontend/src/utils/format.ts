import type { TFunction } from 'i18next';
import { currentLang } from '../i18n/i18n';
import { currentTimeZone } from './timezone';

function locale(): string {
    return currentLang() === 'ja' ? 'ja-JP' : 'en-US';
}

/** "2026-09-17 14:05" style date-time in the user's display zone, or "" for null/invalid. */
export function formatDateTime(iso: string | null | undefined): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return new Intl.DateTimeFormat(locale(), {
        timeZone: currentTimeZone(),
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        hour12: false,
    }).format(d);
}

/** Date only, in the user's display zone. */
export function formatDate(iso: string | null | undefined): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return new Intl.DateTimeFormat(locale(), {
        timeZone: currentTimeZone(),
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
    }).format(d);
}

/** "3 minutes ago" / "in 2 hours" in the active language, or "" for null. */
export function formatRelative(iso: string | null | undefined, now: number = Date.now()): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    const diffSec = Math.round((d.getTime() - now) / 1000);
    const rtf = new Intl.RelativeTimeFormat(locale(), { numeric: 'auto' });
    const abs = Math.abs(diffSec);
    if (abs < 60) return rtf.format(diffSec, 'second');
    if (abs < 3600) return rtf.format(Math.round(diffSec / 60), 'minute');
    if (abs < 86400) return rtf.format(Math.round(diffSec / 3600), 'hour');
    if (abs < 86400 * 30) return rtf.format(Math.round(diffSec / 86400), 'day');
    if (abs < 86400 * 365) return rtf.format(Math.round(diffSec / (86400 * 30)), 'month');
    return rtf.format(Math.round(diffSec / (86400 * 365)), 'year');
}

/** Human readable byte size. */
export function formatBytes(n: number): string {
    if (!Number.isFinite(n) || n < 0) return '';
    if (n < 1024) return `${n} B`;
    const units = ['KB', 'MB', 'GB'];
    let v = n / 1024;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) {
        v /= 1024;
        i++;
    }
    return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

/** Number with locale grouping. */
export function formatNumber(n: number): string {
    return new Intl.NumberFormat(locale()).format(n);
}

/** Builds a display label for a mailbox: display name with address, or the address. */
export function mailboxLabel(mb: { address: string; display_name: string }): string {
    return mb.display_name ? `${mb.display_name} (${mb.address})` : mb.address;
}

/** Version for display: "1.2.3" -> "v1.2.3", "dev" stays. */
export function formatVersion(version: string): string {
    return /^\d/.test(version) ? `v${version}` : version;
}

/** Duration between two timestamps as "1m 23s". */
export function formatDuration(startIso: string | null, endIso: string | null, t: TFunction): string {
    if (!startIso) return '';
    const start = new Date(startIso).getTime();
    const end = endIso ? new Date(endIso).getTime() : Date.now();
    if (Number.isNaN(start) || Number.isNaN(end)) return '';
    const sec = Math.max(0, Math.round((end - start) / 1000));
    if (sec < 60) return t('time.seconds', { count: sec });
    const min = Math.floor(sec / 60);
    if (min < 60) return t('time.minutesSeconds', { minutes: min, seconds: sec % 60 });
    return t('time.hoursMinutes', { hours: Math.floor(min / 60), minutes: min % 60 });
}
