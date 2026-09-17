/**
 * Returns the in-app path a login may continue to, or "/" when the value is
 * missing or unsafe. Only an absolute path of this app is accepted: it must
 * start with a single "/" ("//host" and "/\\host" would leave the site, and
 * a scheme would be an external URL).
 */
export function safeNextPath(next: string | null | undefined): string {
    if (!next || !next.startsWith('/')) return '/';
    if (next.startsWith('//') || next.startsWith('/\\')) return '/';
    return next;
}
