import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router-dom';
import { createMailbox, deleteMailbox, getMailbox, testMailbox, updateMailbox } from '../api/client';
import { Alert } from '../components/ui/Alert';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { ConfirmDialog } from '../components/ui/ConfirmDialog';
import { ErrorState, InlineError } from '../components/ui/ErrorState';
import { CheckboxField, InputField, SelectField, ToggleField } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useToast } from '../components/ui/Toast';
import {
    DEFAULT_FOLDER,
    DEFAULT_IMAP_PORT_PLAIN,
    DEFAULT_IMAP_PORT_SSL,
    DEFAULT_INITIAL_DAYS,
    DEFAULT_RECENT_DAYS,
} from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import type { ImapSecurity, MailboxDTO, MailboxInput } from '../types';
import { errorMessage } from '../utils/errors';

interface FormState {
    address: string;
    display_name: string;
    imap_host: string;
    imap_port: string;
    imap_security: ImapSecurity;
    imap_username: string;
    imap_password: string;
    folder: string;
    enabled: boolean;
    initial_days: string;
    recent_days: string;
}

const EMPTY: FormState = {
    address: '',
    display_name: '',
    imap_host: '',
    imap_port: String(DEFAULT_IMAP_PORT_SSL),
    imap_security: 'ssl',
    imap_username: '',
    imap_password: '',
    folder: DEFAULT_FOLDER,
    enabled: true,
    initial_days: String(DEFAULT_INITIAL_DAYS),
    recent_days: String(DEFAULT_RECENT_DAYS),
};

// Every field is defaulted: the DTO never carries imap_password and the
// server may omit empty optional fields.
function fromDto(mb: MailboxDTO): FormState {
    return {
        address: mb.address ?? '',
        display_name: mb.display_name ?? '',
        imap_host: mb.imap_host ?? '',
        imap_port: mb.imap_port ? String(mb.imap_port) : EMPTY.imap_port,
        imap_security: mb.imap_security ?? 'ssl',
        imap_username: mb.imap_username ?? '',
        imap_password: '',
        folder: mb.folder ?? DEFAULT_FOLDER,
        enabled: mb.enabled ?? true,
        initial_days: mb.initial_days ? String(mb.initial_days) : EMPTY.initial_days,
        recent_days: mb.recent_days ? String(mb.recent_days) : EMPTY.recent_days,
    };
}

type TestResult = { ok: true } | { ok: false; message: string } | null;

export function MailboxEditPage() {
    const { t } = useTranslation();
    const { id: idParam } = useParams();
    const isNew = idParam === undefined || idParam === 'new';
    const id = isNew ? 0 : Number(idParam);
    useDocumentTitle(isNew ? t('mailbox.add') : t('mailbox.edit'));
    const navigate = useNavigate();
    const toast = useToast();
    const existing = useAsync(() => (isNew ? Promise.resolve(null) : getMailbox(id)), [isNew, id]);
    const [form, setForm] = useState<FormState>(EMPTY);
    const [saving, setSaving] = useState(false);
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState<TestResult>(null);
    const [error, setError] = useState<string | null>(null);
    const [confirmDelete, setConfirmDelete] = useState(false);
    const [keepData, setKeepData] = useState(false);
    const [deleting, setDeleting] = useState(false);
    const [showPassword, setShowPassword] = useState(false);

    useEffect(() => {
        if (existing.data) setForm(fromDto(existing.data));
    }, [existing.data]);

    function set<K extends keyof FormState>(key: K, value: FormState[K]) {
        setForm(prev => ({ ...prev, [key]: value }));
        setTestResult(null);
    }

    function setSecurity(value: ImapSecurity) {
        setForm(prev => {
            const wasDefault =
                prev.imap_port === String(DEFAULT_IMAP_PORT_SSL) || prev.imap_port === String(DEFAULT_IMAP_PORT_PLAIN);
            const port = wasDefault
                ? String(value === 'ssl' ? DEFAULT_IMAP_PORT_SSL : DEFAULT_IMAP_PORT_PLAIN)
                : prev.imap_port;
            return { ...prev, imap_security: value, imap_port: port };
        });
        setTestResult(null);
    }

    const port = Number(form.imap_port);
    const portError =
        form.imap_port !== '' && (!Number.isInteger(port) || port < 1 || port > 65535)
            ? t('mailbox.portInvalid')
            : undefined;
    const emailError =
        form.address !== '' && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(form.address)
            ? t('mailbox.addressInvalid')
            : undefined;
    const daysError = (v: string) =>
        v !== '' && (!Number.isInteger(Number(v)) || Number(v) < 1) ? t('mailbox.daysInvalid') : undefined;
    const passwordRequired = isNew && form.imap_password === '';
    const valid =
        form.address.trim() !== '' &&
        !emailError &&
        form.imap_host.trim() !== '' &&
        form.imap_username.trim() !== '' &&
        !portError &&
        !daysError(form.initial_days) &&
        !daysError(form.recent_days) &&
        !passwordRequired;

    function toInput(): MailboxInput {
        return {
            id: isNew ? undefined : id,
            address: form.address.trim(),
            display_name: form.display_name.trim(),
            imap_host: form.imap_host.trim(),
            imap_port: form.imap_port === '' ? undefined : port,
            imap_security: form.imap_security,
            imap_username: form.imap_username.trim(),
            imap_password: form.imap_password === '' ? undefined : form.imap_password,
            folder: form.folder.trim() || DEFAULT_FOLDER,
            enabled: form.enabled,
            initial_days: form.initial_days === '' ? undefined : Number(form.initial_days),
            recent_days: form.recent_days === '' ? undefined : Number(form.recent_days),
        };
    }

    async function test() {
        setTesting(true);
        setTestResult(null);
        try {
            await testMailbox(toInput());
            setTestResult({ ok: true });
        } catch (err) {
            setTestResult({ ok: false, message: errorMessage(err, t) });
        } finally {
            setTesting(false);
        }
    }

    async function handleSubmit(e: FormEvent) {
        e.preventDefault();
        if (!valid || saving) return;
        setSaving(true);
        setError(null);
        try {
            if (isNew) {
                const created = await createMailbox(toInput());
                toast.success(t('mailbox.created', { address: created?.address ?? form.address }));
            } else {
                await updateMailbox(id, toInput());
                toast.success(t('mailbox.updated', { address: form.address }));
            }
            navigate('/settings/mailboxes');
        } catch (err) {
            setError(errorMessage(err, t));
        } finally {
            setSaving(false);
        }
    }

    async function remove() {
        setDeleting(true);
        try {
            await deleteMailbox(id, keepData);
            toast.success(t('mailbox.deleted', { address: form.address }));
            navigate('/settings/mailboxes');
        } catch (err) {
            toast.error(errorMessage(err, t));
            setDeleting(false);
        }
    }

    if (!isNew && existing.loading) return <LoadingBlock />;
    if (!isNew && (existing.error || !existing.data)) {
        return (
            <PageContainer>
                <ErrorState message={errorMessage(existing.error, t)} onRetry={() => void existing.reload()} />
            </PageContainer>
        );
    }

    return (
        <PageContainer>
            <PageHeader
                title={isNew ? t('mailbox.add') : form.address || t('mailbox.edit')}
                crumbs={[
                    { label: t('nav.settings'), to: '/settings' },
                    { label: t('nav.settingsMailboxes'), to: '/settings/mailboxes' },
                    { label: isNew ? t('common.new') : t('common.edit') },
                ]}
            />
            <form onSubmit={handleSubmit} noValidate className='flex flex-col gap-6'>
                <Card>
                    <CardHeader title={t('mailbox.sectionBasic')} />
                    <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                        <InputField
                            label={t('mailbox.address')}
                            type='email'
                            autoComplete='off'
                            value={form.address}
                            onChange={e => set('address', e.target.value)}
                            error={emailError}
                            hint={t('mailbox.addressHint')}
                            required
                        />
                        <InputField
                            label={t('mailbox.displayName')}
                            value={form.display_name}
                            onChange={e => set('display_name', e.target.value)}
                        />
                        <div className='md:col-span-2'>
                            <ToggleField
                                label={t('mailbox.enabled')}
                                hint={t('mailbox.enabledHint')}
                                checked={form.enabled}
                                onChange={v => set('enabled', v)}
                            />
                        </div>
                    </div>
                </Card>

                <Card>
                    <CardHeader title={t('mailbox.sectionImap')} description={t('mailbox.sectionImapHint')} />
                    <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                        <InputField
                            label={t('mailbox.host')}
                            autoComplete='off'
                            value={form.imap_host}
                            onChange={e => set('imap_host', e.target.value)}
                            placeholder='imap.example.com'
                            required
                        />
                        <div className='grid grid-cols-2 gap-4'>
                            <SelectField
                                label={t('mailbox.security')}
                                value={form.imap_security}
                                onChange={e => setSecurity(e.target.value as ImapSecurity)}
                            >
                                <option value='ssl'>{t('imapSecurity.ssl')}</option>
                                <option value='starttls'>{t('imapSecurity.starttls')}</option>
                                <option value='none'>{t('imapSecurity.none')}</option>
                            </SelectField>
                            <InputField
                                label={t('mailbox.port')}
                                type='number'
                                inputMode='numeric'
                                min={1}
                                max={65535}
                                value={form.imap_port}
                                onChange={e => set('imap_port', e.target.value)}
                                error={portError}
                            />
                        </div>
                        <InputField
                            label={t('mailbox.username')}
                            autoComplete='off'
                            value={form.imap_username}
                            onChange={e => set('imap_username', e.target.value)}
                            required
                        />
                        <div>
                            <InputField
                                label={t('mailbox.password')}
                                type={showPassword ? 'text' : 'password'}
                                autoComplete='new-password'
                                value={form.imap_password}
                                onChange={e => set('imap_password', e.target.value)}
                                hint={isNew ? undefined : t('mailbox.passwordKeepHint')}
                                required={isNew}
                            />
                            <CheckboxField
                                className='mt-1'
                                label={t('common.showPassword')}
                                checked={showPassword}
                                onChange={e => setShowPassword(e.target.checked)}
                            />
                        </div>
                        <InputField
                            label={t('mailbox.folder')}
                            autoComplete='off'
                            value={form.folder}
                            onChange={e => set('folder', e.target.value)}
                            placeholder={DEFAULT_FOLDER}
                        />
                    </div>
                    <div className='mt-4 flex flex-col gap-3'>
                        <div>
                            <Button
                                icon='network_check'
                                loading={testing}
                                disabled={!form.imap_host || !form.imap_username || (isNew && !form.imap_password)}
                                onClick={() => void test()}
                            >
                                {t('mailbox.test')}
                            </Button>
                        </div>
                        {testResult && (
                            <Alert tone={testResult.ok ? 'success' : 'danger'}>
                                {testResult.ok
                                    ? t('mailbox.testOkShort')
                                    : t('mailbox.testFailed', { message: testResult.message })}
                            </Alert>
                        )}
                    </div>
                </Card>

                <Card>
                    <CardHeader title={t('mailbox.sectionRange')} description={t('mailbox.sectionRangeHint')} />
                    <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                        <InputField
                            label={t('mailbox.initialDays')}
                            type='number'
                            inputMode='numeric'
                            min={1}
                            value={form.initial_days}
                            onChange={e => set('initial_days', e.target.value)}
                            hint={t('mailbox.initialDaysHint')}
                            error={daysError(form.initial_days)}
                        />
                        <InputField
                            label={t('mailbox.recentDays')}
                            type='number'
                            inputMode='numeric'
                            min={1}
                            value={form.recent_days}
                            onChange={e => set('recent_days', e.target.value)}
                            hint={t('mailbox.recentDaysHint')}
                            error={daysError(form.recent_days)}
                        />
                    </div>
                </Card>

                {error && <InlineError message={error} />}

                <div className='flex flex-col-reverse gap-2 sm:flex-row sm:items-center sm:justify-between'>
                    {!isNew ? (
                        <Button
                            variant='ghost'
                            icon='delete'
                            className='text-danger'
                            onClick={() => setConfirmDelete(true)}
                        >
                            {t('common.delete')}
                        </Button>
                    ) : (
                        <span />
                    )}
                    <div className='flex flex-col-reverse gap-2 sm:flex-row'>
                        <Button onClick={() => navigate('/settings/mailboxes')}>{t('common.cancel')}</Button>
                        <Button type='submit' variant='primary' icon='save' loading={saving} disabled={!valid}>
                            {isNew ? t('common.create') : t('common.save')}
                        </Button>
                    </div>
                </div>
            </form>

            <ConfirmDialog
                open={confirmDelete}
                title={t('mailbox.deleteTitle')}
                message={
                    <div className='flex flex-col gap-3'>
                        <p>{t('mailbox.deleteMessage', { address: form.address })}</p>
                        <CheckboxField
                            label={t('mailbox.keepData')}
                            hint={t('mailbox.keepDataHint')}
                            checked={keepData}
                            onChange={e => setKeepData(e.target.checked)}
                        />
                    </div>
                }
                confirmLabel={t('common.delete')}
                danger
                busy={deleting}
                onConfirm={() => void remove()}
                onCancel={() => setConfirmDelete(false)}
            />
        </PageContainer>
    );
}
