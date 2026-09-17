// Display time zone. Every timestamp from the server is UTC and is shown in
// the signed-in user's IANA zone (users.timezone), never in the browser zone.
// The AuthProvider installs the zone of the current user; the formatters in
// utils/format.ts read it through currentTimeZone().

export const DEFAULT_TIME_ZONE = 'Asia/Tokyo';

let displayTimeZone: string = DEFAULT_TIME_ZONE;

/** True when Intl accepts the zone name on this browser. */
export function isValidTimeZone(zone: string): boolean {
    if (!zone) return false;
    try {
        new Intl.DateTimeFormat('en-US', { timeZone: zone });
        return true;
    } catch {
        return false;
    }
}

/** Installs the zone used by every formatter; an unknown zone falls back to the default. */
export function setDisplayTimeZone(zone: string | null | undefined): void {
    displayTimeZone = zone && isValidTimeZone(zone) ? zone : DEFAULT_TIME_ZONE;
}

export function currentTimeZone(): string {
    return displayTimeZone;
}

// Shown when the browser cannot enumerate zones (Intl.supportedValuesOf is missing).
const FALLBACK_ZONES = [
    'UTC',
    'Asia/Tokyo',
    'Asia/Seoul',
    'Asia/Shanghai',
    'Asia/Hong_Kong',
    'Asia/Taipei',
    'Asia/Singapore',
    'Asia/Bangkok',
    'Asia/Jakarta',
    'Asia/Manila',
    'Asia/Kolkata',
    'Asia/Dubai',
    'Australia/Sydney',
    'Australia/Perth',
    'Pacific/Auckland',
    'Pacific/Honolulu',
    'America/Anchorage',
    'America/Los_Angeles',
    'America/Denver',
    'America/Chicago',
    'America/New_York',
    'America/Toronto',
    'America/Sao_Paulo',
    'Europe/London',
    'Europe/Paris',
    'Europe/Berlin',
    'Europe/Madrid',
    'Europe/Rome',
    'Europe/Moscow',
    'Africa/Johannesburg',
];

/** Every zone the browser knows, or a curated list; the current zone is always included. */
export function listTimeZones(current?: string): string[] {
    let zones: string[];
    const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
    try {
        zones = intl.supportedValuesOf ? [...intl.supportedValuesOf('timeZone')] : [...FALLBACK_ZONES];
    } catch {
        zones = [...FALLBACK_ZONES];
    }
    if (!zones.includes('UTC')) zones.unshift('UTC');
    if (current && !zones.includes(current)) zones.push(current);
    return zones.sort((a, b) => a.localeCompare(b));
}

function zonePart(zone: string, name: 'short' | 'longOffset', at: Date): string {
    try {
        const parts = new Intl.DateTimeFormat('en-US', { timeZone: zone, timeZoneName: name }).formatToParts(at);
        return parts.find(p => p.type === 'timeZoneName')?.value ?? '';
    } catch {
        return '';
    }
}

/** "JST (GMT+09:00)" style label of a zone right now; the abbreviation is omitted when it is only an offset. */
export function zoneLabel(zone: string, at: Date = new Date()): string {
    if (!isValidTimeZone(zone)) return zone;
    const offset = zonePart(zone, 'longOffset', at) || 'GMT';
    const abbrev = zonePart(zone, 'short', at);
    return abbrev && !abbrev.startsWith('GMT') && !abbrev.startsWith('UTC') ? `${abbrev} (${offset})` : offset;
}

/** Offset of the zone at the instant, in minutes east of UTC. */
function offsetMinutes(zone: string, at: Date): number {
    const text = zonePart(zone, 'longOffset', at); // "GMT+09:00", "GMT-03:30" or "GMT"
    const m = /([+-])(\d{2}):?(\d{2})?/.exec(text);
    if (!m) return 0;
    const sign = m[1] === '-' ? -1 : 1;
    return sign * (Number(m[2]) * 60 + Number(m[3] ?? 0));
}

/**
 * Converts a datetime-local input value ("2026-09-17T18:30") that the user
 * typed in the display zone into an ISO instant. Returns null when invalid.
 */
export function localInputToIso(value: string, zone: string = currentTimeZone()): string | null {
    const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(value);
    if (!m) return null;
    const asUtc = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]), Number(m[4]), Number(m[5]), Number(m[6] ?? 0));
    if (Number.isNaN(asUtc)) return null;
    // First guess with the offset at the naive instant, then correct once for DST edges.
    let instant = asUtc - offsetMinutes(zone, new Date(asUtc)) * 60000;
    instant = asUtc - offsetMinutes(zone, new Date(instant)) * 60000;
    return new Date(instant).toISOString();
}
