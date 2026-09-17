// Small client-side format checks. The server validates again; these only
// give immediate feedback next to the field.

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const TIME_RE = /^([01]\d|2[0-3]):[0-5]\d$/;

/** True for "local@domain.tld" shaped text (no attempt at full RFC 5322). */
export function isEmailAddress(value: string): boolean {
    return EMAIL_RE.test(value.trim());
}

/** True for a 24-hour "HH:MM" clock time. */
export function isClockTime(value: string): boolean {
    return TIME_RE.test(value);
}

/** True for an integer within [min, max] written as text. */
export function isIntegerInRange(value: string, min: number, max: number): boolean {
    if (!/^\d+$/.test(value.trim())) return false;
    const n = Number(value);
    return n >= min && n <= max;
}

/** True for an absolute http(s) URL. */
export function isHttpUrl(value: string): boolean {
    try {
        const url = new URL(value.trim());
        return url.protocol === 'http:' || url.protocol === 'https:';
    } catch {
        return false;
    }
}
