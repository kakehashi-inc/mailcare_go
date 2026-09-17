// Typed translation keys: t('nav.dashboard') is checked against ja.json, so a
// typo or a missing key fails "yarn lint" instead of rendering the raw key.
import 'i18next';
import type ja from './ja.json';

declare module 'i18next' {
    interface CustomTypeOptions {
        defaultNS: 'translation';
        resources: {
            translation: typeof ja;
        };
    }
}
