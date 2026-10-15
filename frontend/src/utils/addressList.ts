// Client-side mirror of the server checks of the list settings: the own
// sending IPv4 / IPv6 / host names and the DNS check resolvers (listSetting in
// app/modules/settings.go, Documents/システム仕様.md PUT /api/v1/settings), and
// of the SMTP host of a mailbox, which follows the host name rule.
// The address rules follow net/netip, so a list accepted here is not refused
// on save; the server checks and normalizes again.

import type { MessageKey } from '../api/client';

/** Bound on each own sending list (MaxOwnSendingEntries). */
export const MAX_OWN_SENDING_ENTRIES = 500;
/** Bound on the resolver list (MaxDNSCheckResolvers). */
export const MAX_DNS_CHECK_RESOLVERS = 10;

// The white space strings.TrimSpace removes (unicode.IsSpace).
const TRIM_RE =
    /^[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$/g;
const IPV4_OCTET_RE = /^(0|[1-9]\d{0,2})$/;
const HEX_GROUP_RE = /^[0-9a-fA-F]+/;
const PREFIX_BITS_RE = /^(0|[1-9]\d*)$/;
const HOST_LABEL_RE = /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;
// Characters of an entry quoted in an error (clipEntry).
const CLIP_RUNES = 64;

interface Addr {
    /** 4 bytes for IPv4, 16 for IPv6. */
    bytes: number[];
    is6: boolean;
}

/** One non-blank line of a list and its 1-based row in the text area (blank rows count). */
interface ListEntry {
    line: number;
    value: string;
}

/** Trims the white space strings.TrimSpace removes. */
export function trimSpace(s: string): string {
    return s.replace(TRIM_RE, '');
}

/** Splits a text area into trimmed entries, skipping blank lines but counting them (splitListEntries). */
function splitEntries(text: string): ListEntry[] {
    const out: ListEntry[] = [];
    text.split('\n').forEach((raw, i) => {
        const value = trimSpace(raw);
        if (value !== '') out.push({ line: i + 1, value });
    });
    return out;
}

/** The trimmed non-blank lines of a text area, as sent to the server. */
export function listEntries(text: string): string[] {
    return splitEntries(text).map(e => e.value);
}

/** Four decimal octets without leading zeros (netip parseIPv4); null if invalid. */
function parseIpv4(s: string): number[] | null {
    const parts = s.split('.');
    if (parts.length !== 4) return null;
    const bytes: number[] = [];
    for (const part of parts) {
        if (!IPV4_OCTET_RE.test(part) || Number(part) > 255) return null;
        bytes.push(Number(part));
    }
    return bytes;
}

/**
 * An IPv6 address the way netip parseIPv6 reads it: groups of 1-4 hex digits, at most one "::" standing for
 * at least one zero group, and optionally a dotted IPv4 address as the last 32 bits. A zone is never accepted
 * (the server refuses it in every list). Null if invalid.
 */
function parseIpv6(s: string): number[] | null {
    if (s.includes('%')) return null;
    const bytes: number[] = [];
    let ellipsis = -1;
    let rest = s;
    if (rest.startsWith('::')) {
        ellipsis = 0;
        rest = rest.slice(2);
        if (rest === '') return new Array<number>(16).fill(0);
    }
    while (bytes.length < 16) {
        const digits = HEX_GROUP_RE.exec(rest)?.[0] ?? '';
        if (digits === '') return null;
        if (rest[digits.length] === '.') {
            // Embedded IPv4: only after "::" or in place of the last two groups.
            if ((ellipsis < 0 && bytes.length !== 12) || bytes.length + 4 > 16) return null;
            const v4 = parseIpv4(rest);
            if (!v4) return null;
            bytes.push(...v4);
            rest = '';
            break;
        }
        if (digits.length > 4) return null;
        const group = parseInt(digits, 16);
        bytes.push(group >> 8, group & 0xff);
        rest = rest.slice(digits.length);
        if (rest === '') break;
        if (rest[0] !== ':' || rest.length === 1) return null;
        rest = rest.slice(1);
        if (rest[0] === ':') {
            if (ellipsis >= 0) return null;
            ellipsis = bytes.length;
            rest = rest.slice(1);
            if (rest === '') break;
        }
    }
    if (rest !== '') return null;
    if (bytes.length < 16) {
        if (ellipsis < 0) return null;
        bytes.splice(ellipsis, 0, ...new Array<number>(16 - bytes.length).fill(0));
    } else if (ellipsis >= 0) {
        return null;
    }
    return bytes;
}

/** netip.ParseAddr without zones: the first "." or ":" decides between IPv4 and IPv6. */
function parseAddr(s: string): Addr | null {
    const sep = /[.:%]/.exec(s)?.[0];
    if (sep === '.') {
        const bytes = parseIpv4(s);
        return bytes && { bytes, is6: false };
    }
    if (sep === ':') {
        const bytes = parseIpv6(s);
        return bytes && { bytes, is6: true };
    }
    return null;
}

/** Not loopback, not unspecified and not IPv4-mapped (usableIP; zones are refused while parsing). */
function usable(a: Addr): boolean {
    const zero = (from: number, to: number) => a.bytes.slice(from, to).every(b => b === 0);
    if (!a.is6) return a.bytes[0] !== 127 && !zero(0, 4);
    const loopback = zero(0, 15) && a.bytes[15] === 1;
    const mapped = zero(0, 10) && a.bytes[10] === 0xff && a.bytes[11] === 0xff;
    return !loopback && !zero(0, 16) && !mapped;
}

function addrKey(a: Addr): string {
    return a.bytes.join('.');
}

/**
 * The comparison key of an IPv4 (want6 false) or IPv6 address or CIDR, or null if it is not one
 * (normalizeIPOrPrefix): a CIDR is masked to its network address, which must be usable, and a CIDR of one
 * address equals the address.
 */
function ipOrPrefixKey(s: string, want6: boolean): string | null {
    const slash = s.lastIndexOf('/');
    if (slash < 0) {
        const a = parseAddr(s);
        return a && a.is6 === want6 && usable(a) ? addrKey(a) : null;
    }
    const a = parseAddr(s.slice(0, slash));
    const bitsText = s.slice(slash + 1);
    if (!a || !PREFIX_BITS_RE.test(bitsText)) return null;
    const bits = Number(bitsText);
    const maxBits = a.bytes.length * 8;
    if (bits > maxBits) return null;
    const masked: Addr = {
        bytes: a.bytes.map((b, i) => {
            const keep = bits - i * 8;
            return keep >= 8 ? b : keep <= 0 ? 0 : b & (0xff << (8 - keep)) & 0xff;
        }),
        is6: a.is6,
    };
    if (masked.is6 !== want6 || !usable(masked)) return null;
    return bits === maxBits ? addrKey(masked) : `${addrKey(masked)}/${bits}`;
}

/**
 * The comparison key of a host name (lower-case, one trailing dot removed), or null if it is not one
 * (normalizeOwnHost): labels of 1-63 ASCII letters, digits and hyphens (not at either end), 253 characters at
 * most, a last label that is not digits only, and none of the loopback names. The same rule checks the SMTP
 * host of a mailbox.
 */
export function hostKey(s: string): string | null {
    const h = s.endsWith('.') ? s.slice(0, -1) : s;
    if (h === '' || h.length > 253) return null;
    const labels = h.split('.');
    if (!labels.every(label => HOST_LABEL_RE.test(label))) return null;
    if (/^\d+$/.test(labels[labels.length - 1])) return null;
    const lower = h.toLowerCase();
    if (lower === 'localhost' || lower.endsWith('.localhost') || lower === 'localhost.localdomain') return null;
    return lower;
}

/** The comparison key of a resolver: a usable IPv4 or IPv6 address without port or CIDR (normalizeResolver). */
function resolverKey(s: string): string | null {
    const a = parseAddr(s);
    return a && usable(a) ? addrKey(a) : null;
}

/** An error of a list in the shape of the server's (ApiError key and params). */
export interface ListError {
    key: MessageKey;
    params: Record<string, unknown>;
}

/** The rules of one list setting: how an entry is compared (null = invalid), its bound and its errors. */
export interface ListRule {
    key: (entry: string) => string | null;
    max: number;
    invalid: MessageKey;
    tooMany: MessageKey;
}

export const OWN_SENDING_IPV4: ListRule = {
    key: s => ipOrPrefixKey(s, false),
    max: MAX_OWN_SENDING_ENTRIES,
    invalid: 'validation.ownSending.ipv4Invalid',
    tooMany: 'validation.ownSending.tooMany',
};

export const OWN_SENDING_IPV6: ListRule = {
    key: s => ipOrPrefixKey(s, true),
    max: MAX_OWN_SENDING_ENTRIES,
    invalid: 'validation.ownSending.ipv6Invalid',
    tooMany: 'validation.ownSending.tooMany',
};

export const OWN_SENDING_HOSTS: ListRule = {
    key: hostKey,
    max: MAX_OWN_SENDING_ENTRIES,
    invalid: 'validation.ownSending.hostInvalid',
    tooMany: 'validation.ownSending.tooMany',
};

export const DNS_CHECK_RESOLVERS: ListRule = {
    key: resolverKey,
    max: MAX_DNS_CHECK_RESOLVERS,
    invalid: 'validation.dnsCheck.resolverInvalid',
    tooMany: 'validation.dnsCheck.resolversTooMany',
};

/**
 * Checks a text area of a list setting the way the server does on save: the first invalid line is the error
 * (line = its row, blank rows counted; value clipped like the server's), then the number of entries left after
 * removing duplicates must not exceed the bound. Null when the list can be saved.
 */
export function checkList(text: string, rule: ListRule): ListError | null {
    const seen = new Set<string>();
    for (const e of splitEntries(text)) {
        const key = rule.key(e.value);
        if (key === null) {
            const runes = [...e.value];
            const value = runes.length > CLIP_RUNES ? `${runes.slice(0, CLIP_RUNES).join('')}...` : e.value;
            return { key: rule.invalid, params: { line: e.line, value } };
        }
        seen.add(key);
    }
    return seen.size > rule.max ? { key: rule.tooMany, params: { max: rule.max } } : null;
}
