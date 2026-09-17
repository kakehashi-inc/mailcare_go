// Tunable frontend settings collected in one place so they are easy to find.

// --- Language ---

// Languages the UI ships translations for (see src/i18n/). The first entry is
// the fallback when neither the saved choice nor the browser language matches.
export const SUPPORTED_LANGS = ['ja', 'en'] as const;
export type Lang = (typeof SUPPORTED_LANGS)[number];
export const DEFAULT_LANG: Lang = SUPPORTED_LANGS[0];

// localStorage key that remembers an explicitly chosen language. It is only
// written when the user picks a language from the menu, so browser detection
// keeps working for everyone else.
export const LANG_STORAGE_KEY = 'develop_app.lang';
