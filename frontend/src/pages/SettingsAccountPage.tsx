import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { changeMyPassword } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { RoleBadge } from '../components/domain/StatusBadges';
import { Alert } from '../components/ui/Alert';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { DescriptionList } from '../components/ui/DescriptionList';
import { InlineError } from '../components/ui/ErrorState';
import { InputField } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { useToast } from '../components/ui/Toast';
import { MIN_PASSWORD_LENGTH } from '../constants';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { errorMessage } from '../utils/errors';

export function SettingsAccountPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.settingsAccount'));
    const { me, refresh } = useAuth();
    const toast = useToast();
    const [current, setCurrent] = useState('');
    const [next, setNext] = useState('');
    const [confirm, setConfirm] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);

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
            toast.success(t('account.passwordChanged'));
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
                title={t('nav.settingsAccount')}
                crumbs={[{ label: t('nav.settings'), to: '/settings' }, { label: t('nav.settingsAccount') }]}
            />
            <div className='flex flex-col gap-6'>
                {me && (
                    <Card>
                        <CardHeader title={t('account.profile')} />
                        <DescriptionList
                            items={[
                                { label: t('user.username'), value: me.user.username },
                                { label: t('user.displayName'), value: me.user.display_name || '-' },
                                { label: t('user.role'), value: <RoleBadge role={me.user.role} /> },
                            ]}
                        />
                    </Card>
                )}
                <Card>
                    <CardHeader title={t('account.changePassword')} description={t('account.changePasswordHint')} />
                    <Alert tone='info' className='mb-4'>
                        {t('account.sessionNote')}
                    </Alert>
                    <form onSubmit={handleSubmit} noValidate className='flex max-w-md flex-col gap-4'>
                        <InputField
                            label={t('account.currentPassword')}
                            type='password'
                            autoComplete='current-password'
                            value={current}
                            onChange={e => setCurrent(e.target.value)}
                            required
                        />
                        <InputField
                            label={t('account.newPassword')}
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
                                {t('account.changePassword')}
                            </Button>
                        </div>
                    </form>
                </Card>
            </div>
        </PageContainer>
    );
}
