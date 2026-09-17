import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/AuthProvider';
import { UserPreferenceFields } from '../components/domain/UserPreferenceFields';
import { Alert } from '../components/ui/Alert';
import { Button } from '../components/ui/Button';
import { InlineError } from '../components/ui/ErrorState';
import { InputField } from '../components/ui/Field';
import { DEFAULT_LANG, MIN_PASSWORD_LENGTH, type Lang } from '../constants';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { applyLanguage } from '../i18n/i18n';
import { errorMessage } from '../utils/errors';
import { applyTheme, DEFAULT_THEME, type Theme } from '../utils/theme';
import { DEFAULT_TIME_ZONE } from '../utils/timezone';
import { isEmailAddress } from '../utils/validate';
import { AuthShell } from './AuthShell';

/**
 * First administrator. Field order is shared with the user forms: username,
 * display name, email, language, time zone, theme, password. Language and
 * theme apply to the page as soon as they are chosen.
 */
export function SetupPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('setup.title'));
    const { setup } = useAuth();
    const navigate = useNavigate();
    const [username, setUsername] = useState('');
    const [displayName, setDisplayName] = useState('');
    const [email, setEmail] = useState('');
    const [language, setLanguage] = useState<Lang>(DEFAULT_LANG);
    const [timezone, setTimezone] = useState(DEFAULT_TIME_ZONE);
    const [theme, setTheme] = useState<Theme>(DEFAULT_THEME);
    const [password, setPassword] = useState('');
    const [confirm, setConfirm] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const emailInvalid = email.trim() !== '' && !isEmailAddress(email);
    const tooShort = password !== '' && password.length < MIN_PASSWORD_LENGTH;
    const mismatch = confirm !== '' && confirm !== password;
    const canSubmit =
        username.trim() !== '' && !emailInvalid && password.length >= MIN_PASSWORD_LENGTH && confirm === password;

    async function handleSubmit(e: FormEvent) {
        e.preventDefault();
        if (!canSubmit || submitting) return;
        setSubmitting(true);
        setError(null);
        try {
            await setup({
                username: username.trim(),
                display_name: displayName.trim(),
                email: email.trim(),
                language,
                timezone,
                theme,
                password,
            });
            navigate('/', { replace: true });
        } catch (err) {
            setError(errorMessage(err, t));
        } finally {
            setSubmitting(false);
        }
    }

    return (
        <AuthShell title={t('setup.title')} description={t('setup.description')}>
            <Alert tone='info' className='mb-4'>
                {t('setup.hint')}
            </Alert>
            <form onSubmit={handleSubmit} noValidate className='space-y-4'>
                <InputField
                    label={t('user.username')}
                    autoComplete='username'
                    value={username}
                    onChange={e => setUsername(e.target.value)}
                    hint={t('user.usernameHint')}
                    autoFocus
                    required
                />
                <InputField
                    label={t('user.displayName')}
                    autoComplete='name'
                    value={displayName}
                    onChange={e => setDisplayName(e.target.value)}
                />
                <InputField
                    label={t('user.email')}
                    type='email'
                    inputMode='email'
                    autoComplete='email'
                    value={email}
                    onChange={e => setEmail(e.target.value)}
                    hint={t('user.emailHint')}
                    error={emailInvalid ? t('user.emailInvalid') : undefined}
                />
                <UserPreferenceFields
                    language={language}
                    timezone={timezone}
                    theme={theme}
                    onLanguage={v => {
                        setLanguage(v);
                        void applyLanguage(v);
                    }}
                    onTimezone={setTimezone}
                    onTheme={v => {
                        setTheme(v);
                        applyTheme(v);
                    }}
                />
                <InputField
                    label={t('user.password')}
                    type='password'
                    autoComplete='new-password'
                    value={password}
                    onChange={e => setPassword(e.target.value)}
                    hint={t('user.passwordHint', { min: MIN_PASSWORD_LENGTH })}
                    error={tooShort ? t('user.passwordTooShort', { min: MIN_PASSWORD_LENGTH }) : undefined}
                    required
                />
                <InputField
                    label={t('user.passwordConfirm')}
                    type='password'
                    autoComplete='new-password'
                    value={confirm}
                    onChange={e => setConfirm(e.target.value)}
                    error={mismatch ? t('user.passwordMismatch') : undefined}
                    required
                />
                {error && <InlineError message={error} />}
                <Button
                    type='submit'
                    variant='primary'
                    block
                    loading={submitting}
                    disabled={!canSubmit}
                    icon='person_add'
                >
                    {t('setup.submit')}
                </Button>
            </form>
        </AuthShell>
    );
}
