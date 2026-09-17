import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import { DEFAULT_LANG, SUPPORTED_LANGS, type Lang } from '../constants';
import en from './en.json';
import ja from './ja.json';

export function isLang(value: unknown): value is Lang {
    return SUPPORTED_LANGS.includes(value as Lang);
}

// The UI always starts in the default language (also on /login and /setup);
// the AuthProvider switches to the signed-in user's language (users.language)
// after login. Neither the browser language nor local storage is consulted.
i18n.use(initReactI18next).init({
    resources: { ja: { translation: ja }, en: { translation: en } },
    lng: DEFAULT_LANG,
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

/** Switches the UI language; an unknown value selects the default. */
export function applyLanguage(lng: string | null | undefined): Promise<unknown> {
    const next = isLang(lng) ? lng : DEFAULT_LANG;
    if (next === currentLang()) return Promise.resolve();
    return i18n.changeLanguage(next);
}

/** The currently active language, narrowed to one the UI supports. */
export function currentLang(): Lang {
    const base = i18n.language.toLowerCase().split('-')[0];
    return isLang(base) ? base : DEFAULT_LANG;
}

export default i18n;
