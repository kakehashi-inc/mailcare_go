import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import { DEFAULT_LANG, LANG_STORAGE_KEY, SUPPORTED_LANGS, type Lang } from '../constants';
import en from './en.json';
import ja from './ja.json';

function isLang(value: unknown): value is Lang {
    return SUPPORTED_LANGS.includes(value as Lang);
}

/**
 * Initial language: an explicit choice saved earlier wins, then the browser's
 * preferred languages in order, then the default.
 */
function detectLang(): Lang {
    try {
        const stored = localStorage.getItem(LANG_STORAGE_KEY);
        if (isLang(stored)) return stored;
    } catch {
        // Storage can be unavailable (private mode, blocked site data).
    }
    const candidates = typeof navigator !== 'undefined' ? navigator.languages : [];
    for (const tag of candidates) {
        const base = tag.toLowerCase().split('-')[0];
        if (isLang(base)) return base;
    }
    return DEFAULT_LANG;
}

i18n.use(initReactI18next).init({
    resources: { ja: { translation: ja }, en: { translation: en } },
    lng: detectLang(),
    fallbackLng: DEFAULT_LANG,
    supportedLngs: [...SUPPORTED_LANGS],
    interpolation: { escapeValue: false }, // React already escapes
});

// Keep <html lang> in sync for screen readers, hyphenation and font selection.
function applyDocumentLang(lng: string) {
    document.documentElement.lang = lng;
}
applyDocumentLang(i18n.language);
i18n.on('languageChanged', applyDocumentLang);

/** Switches the UI language and remembers the choice for the next visit. */
export function changeLanguage(lng: Lang): Promise<unknown> {
    try {
        localStorage.setItem(LANG_STORAGE_KEY, lng);
    } catch {
        // Not fatal: the choice simply is not remembered.
    }
    return i18n.changeLanguage(lng);
}

/** The currently active language, narrowed to one the UI supports. */
export function currentLang(): Lang {
    const base = i18n.language.toLowerCase().split('-')[0];
    return isLang(base) ? base : DEFAULT_LANG;
}

export default i18n;
