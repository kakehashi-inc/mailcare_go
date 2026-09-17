import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { changeMyPassword, updateMyProfile } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { RoleBadge } from '../components/domain/StatusBadges';
import { UserPreferenceFields } from '../components/domain/UserPreferenceFields';
import { Alert } from '../components/ui/Alert';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { InlineError } from '../components/ui/ErrorState';
import { InputField } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { useToast } from '../components/ui/Toast';
import { DEFAULT_LANG, MIN_PASSWORD_LENGTH, type Lang } from '../constants';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { isLang } from '../i18n/i18n';
import type { ProfileInput, UserDTO } from '../types';
import { errorMessage } from '../utils/errors';
import { DEFAULT_THEME, isTheme, type Theme } from '../utils/theme';
import { DEFAULT_TIME_ZONE } from '../utils/timezone';
import { isEmailAddress } from '../utils/validate';

interface ProfileForm {
    display_name: string;
    email: string;
    language: Lang;
    timezone: string;
    theme: Theme;
}

function fromUser(user: UserDTO | undefined): ProfileForm {
    return {
        display_name: user?.display_name ?? '',
        email: user?.email ?? '',
        language: isLang(user?.language) ? user.language : DEFAULT_LANG,
        timezone: user?.timezone || DEFAULT_TIME_ZONE,
        theme: isTheme(user?.theme) ? user.theme : DEFAULT_THEME,
    };
}

/** Only the fields that differ from the saved user are sent. */
function diff(form: ProfileForm, saved: ProfileForm): ProfileInput {
    const out: ProfileInput = {};
    if (form.display_name.trim() !== saved.display_name) out.display_name = form.display_name.trim();
    if (form.email.trim() !== saved.email) out.email = form.email.trim();
    if (form.language !== saved.language) out.language = form.language;
    if (form.timezone !== saved.timezone) out.timezone = form.timezone;
    if (form.theme !== saved.theme) out.theme = form.theme;
    return out;
}

/**
 * The signed-in user's own settings: one card with display name, email,
 * language, time zone and theme (field order shared with the user forms) and
 * a separate password card. After a save the user is reloaded, which
 * re-applies language, theme and time zone through the AuthProvider.
 */
export function SettingsProfilePage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.settingsProfile'));
    const { me, refresh } = useAuth();
    const toast = useToast();
    const saved = fromUser(me?.user);
    const [form, setForm] = useState<ProfileForm>(saved);
    const [saving, setSaving] = useState(false);
    const [saveError, setSaveError] = useState<string | null>(null);
    const [current, setCurrent] = useState('');
    const [next, setNext] = useState('');
    const [confirm, setConfirm] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);

    // Reset the form when the user is reloaded (after a save, or a change made elsewhere).
    useEffect(() => {
        setForm(fromUser(me?.user));
    }, [me]);

    function set<K extends keyof ProfileForm>(key: K, value: ProfileForm[K]) {
        setForm(prev => ({ ...prev, [key]: value }));
    }

    const changes = diff(form, saved);
    const emailInvalid = form.email.trim() !== '' && !isEmailAddress(form.email);
    const canSave = Object.keys(changes).length > 0 && !emailInvalid && !saving;

    async function saveProfile(e: FormEvent) {
        e.preventDefault();
        if (!canSave) return;
        setSaving(true);
        setSaveError(null);
        try {
            await updateMyProfile(changes);
            toast.success(t('profile.saved'));
            await refresh();
        } catch (err) {
            setSaveError(errorMessage(err, t));
        } finally {
            setSaving(false);
        }
    }

    const tooShort = next !== '' && next.length < MIN_PASSWORD_LENGTH;
    const mismatch = confirm !== '' && confirm !== next;
    const canSubmit = current !== '' && next.length >= MIN_PASSWORD_LENGTH && confirm === next && !submitting;

    async function handleSubmit(e: FormEvent) {
        e.preventDefault();
        if (!canSubmit) return;
        setSubmitting(true);
        setError(null);
        try {
            await changeMyPassword(current, next);
            toast.success(t('profile.passwordChanged'));
            setCurrent('');
            setNext('');
            setConfirm('');
            // The server invalidates sessions on password change; refresh to
            // find out whether ours survived.
            await refresh();
        } catch (err) {
            setError(errorMessage(err, t));
        } finally {
            setSubmitting(false);
        }
    }

    return (
        <PageContainer>
            <PageHeader
                title={t('nav.settingsProfile')}
                crumbs={[{ label: t('nav.settings'), to: '/settings' }, { label: t('nav.settingsProfile') }]}
            />
            <div className='flex flex-col gap-6'>
                <Card>
                    <CardHeader title={t('profile.profile')} description={t('profile.profileHint')} />
                    <form onSubmit={saveProfile} noValidate className='flex max-w-md flex-col gap-4'>
                        <InputField label={t('user.username')} value={me?.user.username ?? ''} readOnly disabled />
                        <InputField
                            label={t('user.displayName')}
                            autoComplete='name'
                            value={form.display_name}
                            onChange={e => set('display_name', e.target.value)}
                        />
                        <InputField
                            label={t('user.email')}
                            type='email'
                            inputMode='email'
                            autoComplete='email'
                            value={form.email}
                            onChange={e => set('email', e.target.value)}
                            placeholder='you@example.com'
                            hint={t('profile.emailHint')}
                            error={emailInvalid ? t('user.emailInvalid') : undefined}
                        />
                        <UserPreferenceFields
                            language={form.language}
                            timezone={form.timezone}
                            theme={form.theme}
                            onLanguage={v => set('language', v)}
                            onTimezone={v => set('timezone', v)}
                            onTheme={v => set('theme', v)}
                        />
                        {me && (
                            <p className='text-sm text-muted'>
                                {t('user.role')}: <RoleBadge role={me.user.role} />
                            </p>
                        )}
                        {saveError && <InlineError message={saveError} />}
                        <div>
                            <Button type='submit' variant='primary' icon='save' loading={saving} disabled={!canSave}>
                                {t('common.save')}
                            </Button>
                        </div>
                    </form>
                </Card>
                <Card>
                    <CardHeader title={t('profile.changePassword')} description={t('profile.changePasswordHint')} />
                    <Alert tone='info' className='mb-4'>
                        {t('profile.sessionNote')}
                    </Alert>
                    <form onSubmit={handleSubmit} noValidate className='flex max-w-md flex-col gap-4'>
                        <InputField
                            label={t('profile.currentPassword')}
                            type='password'
                            autoComplete='current-password'
                            value={current}
                            onChange={e => setCurrent(e.target.value)}
                            required
                        />
                        <InputField
                            label={t('profile.newPassword')}
                            type='password'
                            autoComplete='new-password'
                            value={next}
                            onChange={e => setNext(e.target.value)}
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
                        <div>
                            <Button
                                type='submit'
                                variant='primary'
                                icon='lock_reset'
                                loading={submitting}
                                disabled={!canSubmit}
                            >
                                {t('profile.changePassword')}
                            </Button>
                        </div>
                    </form>
                </Card>
            </div>
        </PageContainer>
    );
}
