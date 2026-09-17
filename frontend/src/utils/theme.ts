// Color theme of the signed-in user. "auto" follows the browser preference
// through the light-dark() tokens (color-scheme: light dark on :root);
// "light" / "dark" force one scheme via the data-theme attribute on <html>
// (see scss/tailwind.css). Before login the theme is auto.

export const THEMES = ['auto', 'light', 'dark'] as const;
export type Theme = (typeof THEMES)[number];
export const DEFAULT_THEME: Theme = 'auto';

export function isTheme(value: unknown): value is Theme {
    return THEMES.includes(value as Theme);
}

/** Applies the theme to the document; unknown values fall back to auto. */
export function applyTheme(theme: string | null | undefined): void {
    const value = isTheme(theme) ? theme : DEFAULT_THEME;
    const root = document.documentElement;
    if (value === 'auto') root.removeAttribute('data-theme');
    else root.setAttribute('data-theme', value);
}
