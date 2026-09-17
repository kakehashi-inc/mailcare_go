import { useTranslation } from 'react-i18next';
import { SUPPORTED_LANGS, type Lang } from '../../constants';
import { THEMES, type Theme } from '../../utils/theme';
import { listTimeZones, zoneLabel } from '../../utils/timezone';
import { SelectField } from '../ui/Field';

interface UserPreferenceFieldsProps {
    language: Lang;
    timezone: string;
    theme: Theme;
    onLanguage: (value: Lang) => void;
    onTimezone: (value: string) => void;
    onTheme: (value: Theme) => void;
}

/**
 * The three preference selects shared by the setup, profile and user forms,
 * always in the same order: language, time zone, theme.
 */
export function UserPreferenceFields({
    language,
    timezone,
    theme,
    onLanguage,
    onTimezone,
    onTheme,
}: UserPreferenceFieldsProps) {
    const { t } = useTranslation();
    const zones = listTimeZones(timezone);
    return (
        <>
            <SelectField
                label={t('user.language')}
                value={language}
                onChange={e => onLanguage(e.target.value as Lang)}
                hint={t('user.languageHint')}
            >
                {SUPPORTED_LANGS.map(code => (
                    <option key={code} value={code}>
                        {t(`lang.${code}`)}
                    </option>
                ))}
            </SelectField>
            <SelectField
                label={t('user.timezone')}
                value={timezone}
                onChange={e => onTimezone(e.target.value)}
                hint={`${t('user.timezoneHint')} (${zoneLabel(timezone)})`}
            >
                {zones.map(z => (
                    <option key={z} value={z}>
                        {z}
                    </option>
                ))}
            </SelectField>
            <SelectField
                label={t('user.theme')}
                value={theme}
                onChange={e => onTheme(e.target.value as Theme)}
                hint={t('user.themeHint')}
            >
                {THEMES.map(v => (
                    <option key={v} value={v}>
                        {t(`theme.${v}`)}
                    </option>
                ))}
            </SelectField>
        </>
    );
}
