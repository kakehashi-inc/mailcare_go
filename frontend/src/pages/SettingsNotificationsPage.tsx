import { useEffect, useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
    ApiError,
    getNotificationSettings,
    getSettings,
    sendNotificationNow,
    sendTestNotification,
    updateNotificationSettings,
} from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { Alert } from '../components/ui/Alert';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { DateTime } from '../components/ui/DateTime';
import { DescriptionList } from '../components/ui/DescriptionList';
import { ErrorState } from '../components/ui/ErrorState';
import { CheckboxField, InputField, SelectField, ToggleField } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useToast } from '../components/ui/Toast';
import {
    DEFAULT_NOTIFY_TIME,
    DEFAULT_SMTP_PORT,
    NOTIFY_INTERVAL_MAX_DAYS,
    NOTIFY_INTERVAL_MIN_DAYS,
} from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import type { NotificationSettingsDTO, NotificationSettingsInput, SmtpSecurity } from '../types';
import { isClockTime, isEmailAddress, isHttpUrl, isIntegerInRange } from '../utils/validate';

interface FormState {
    smtp_host: string;
    smtp_port: string;
    smtp_security: SmtpSecurity;
    smtp_username: string;
    /** Only sent when typed; an empty field keeps the stored password. */
    smtp_password: string;
    smtp_from: string;
    public_base_url: string;
    notify_enabled: boolean;
    notify_time: string;
    notify_interval_days: number;
    notify_user_ids: number[];
}

const SMTP_KEYS = ['smtp_host', 'smtp_port', 'smtp_security', 'smtp_username', 'smtp_password', 'smtp_from'] as const;

/** The keys that name the server the stored password belongs to; changing one of them needs the password again. */
const CONNECTION_KEYS = ['smtp_host', 'smtp_port', 'smtp_security', 'smtp_username'] as const;

const INTERVAL_OPTIONS = Array.from(
    { length: NOTIFY_INTERVAL_MAX_DAYS - NOTIFY_INTERVAL_MIN_DAYS + 1 },
    (_, i) => NOTIFY_INTERVAL_MIN_DAYS + i
);

// Every field is defaulted: the server may omit empty optional fields.
function fromDto(dto: NotificationSettingsDTO): FormState {
    return {
        smtp_host: dto.smtp_host ?? '',
        smtp_port: dto.smtp_port ? String(dto.smtp_port) : String(DEFAULT_SMTP_PORT),
        smtp_security: dto.smtp_security ?? 'starttls',
        smtp_username: dto.smtp_username ?? '',
        smtp_password: '',
        smtp_from: dto.smtp_from ?? '',
        public_base_url: dto.public_base_url ?? '',
        notify_enabled: dto.notify_enabled ?? false,
        notify_time: dto.notify_time || DEFAULT_NOTIFY_TIME,
        notify_interval_days: dto.notify_interval_days || NOTIFY_INTERVAL_MIN_DAYS,
        notify_user_ids: [...(dto.notify_user_ids ?? [])],
    };
}

function sameIds(a: number[], b: number[]): boolean {
    if (a.length !== b.length) return false;
    const sa = [...a].sort((x, y) => x - y);
    const sb = [...b].sort((x, y) => x - y);
    return sa.every((v, i) => v === sb[i]);
}

/** The fields that differ from the saved settings; password only when typed. */
function diff(form: FormState, saved: FormState): NotificationSettingsInput {
    const out: NotificationSettingsInput = {};
    if (form.smtp_host.trim() !== saved.smtp_host) out.smtp_host = form.smtp_host.trim();
    if (form.smtp_port !== saved.smtp_port) out.smtp_port = Number(form.smtp_port);
    if (form.smtp_security !== saved.smtp_security) out.smtp_security = form.smtp_security;
    if (form.smtp_username.trim() !== saved.smtp_username) out.smtp_username = form.smtp_username.trim();
    if (form.smtp_password !== '') out.smtp_password = form.smtp_password;
    if (form.smtp_from.trim() !== saved.smtp_from) out.smtp_from = form.smtp_from.trim();
    if (form.public_base_url.trim() !== saved.public_base_url) out.public_base_url = form.public_base_url.trim();
    if (form.notify_enabled !== saved.notify_enabled) out.notify_enabled = form.notify_enabled;
    if (form.notify_time !== saved.notify_time) out.notify_time = form.notify_time;
    if (form.notify_interval_days !== saved.notify_interval_days) {
        out.notify_interval_days = form.notify_interval_days;
    }
    if (!sameIds(form.notify_user_ids, saved.notify_user_ids)) out.notify_user_ids = [...form.notify_user_ids];
    return out;
}

type Blocker = 'disabled' | 'smtpIncomplete' | 'noRecipients' | null;

/** Why the server would skip sending, judged from the saved settings (design document 7.4). */
function blockerOf(dto: NotificationSettingsDTO): Blocker {
    if (!dto.notify_enabled) return 'disabled';
    if (!dto.smtp_host || !dto.smtp_from) return 'smtpIncomplete';
    const selected = new Set(dto.notify_user_ids ?? []);
    const withEmail = (dto.recipients ?? []).some(r => selected.has(r.id) && r.email);
    if (!withEmail) return 'noRecipients';
    return null;
}

export function SettingsNotificationsPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.settingsNotifications'));
    const { me } = useAuth();
    const toast = useToast();
    const settings = useAsync(getNotificationSettings, []);
    const general = useAsync(getSettings, []);
    const [form, setForm] = useState<FormState | null>(null);
    const [saved, setSaved] = useState<FormState | null>(null);
    const [saving, setSaving] = useState(false);
    const [showPassword, setShowPassword] = useState(false);
    const [testTo, setTestTo] = useState('');
    const [testing, setTesting] = useState(false);
    const [sending, setSending] = useState(false);
    const recipientsId = useId();

    useEffect(() => {
        if (!settings.data) return;
        const state = fromDto(settings.data);
        setForm(state);
        setSaved(state);
    }, [settings.data]);

    function set<K extends keyof FormState>(key: K, value: FormState[K]) {
        setForm(prev => (prev ? { ...prev, [key]: value } : prev));
    }

    function toggleRecipient(id: number, checked: boolean) {
        setForm(prev => {
            if (!prev) return prev;
            const ids = prev.notify_user_ids.filter(x => x !== id);
            return { ...prev, notify_user_ids: checked ? [...ids, id] : ids };
        });
    }

    async function save() {
        if (!form || !saved) return;
        setSaving(true);
        try {
            await updateNotificationSettings(diff(form, saved));
            toast.success(t('result.setting.saved'));
            setShowPassword(false);
            await settings.reload();
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setSaving(false);
        }
    }

    async function test() {
        const to = testTo.trim();
        setTesting(true);
        try {
            await sendTestNotification(to || undefined);
            toast.success(t('result.notification.testSent', { to: to || me?.user.email || '' }));
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setTesting(false);
        }
    }

    async function sendNow() {
        setSending(true);
        try {
            const r = await sendNotificationNow();
            if (r.created) toast.success(t('result.notification.sendQueued'));
            else toast.info(t('result.notification.sendAlreadyQueued'));
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setSending(false);
        }
    }

    if (settings.loading || (!settings.error && (!form || !saved))) return <LoadingBlock />;
    if (settings.error || !settings.data || !form || !saved) {
        return (
            <PageContainer>
                <ErrorState
                    message={t(settings.error?.key ?? 'system.internal', settings.error?.params)}
                    onRetry={() => void settings.reload()}
                />
            </PageContainer>
        );
    }

    const dto = settings.data;
    const changes = diff(form, saved);
    const dirty = Object.keys(changes).length > 0;
    const smtpDirty = SMTP_KEYS.some(key => key in changes);
    const portError = isIntegerInRange(form.smtp_port, 1, 65535)
        ? undefined
        : t('validation.common.numberOutOfRange', { min: 1, max: 65535 });
    const fromError =
        form.smtp_from.trim() !== '' && !isEmailAddress(form.smtp_from)
            ? t('validation.common.emailFormat')
            : undefined;
    const timeError = isClockTime(form.notify_time) ? undefined : t('validation.common.timeFormat');
    const urlError =
        form.public_base_url.trim() !== '' && !isHttpUrl(form.public_base_url)
            ? t('validation.common.urlFormat')
            : undefined;
    const testToError =
        testTo.trim() !== '' && !isEmailAddress(testTo) ? t('validation.common.emailFormat') : undefined;
    // The server refuses to point a stored password at another server: a
    // change of the connection settings must carry the password.
    const connectionDirty = CONNECTION_KEYS.some(key => key in changes);
    const passwordError =
        connectionDirty && dto.smtp_password_set && form.smtp_password === ''
            ? t('validation.connection.passwordRequiredOnChange')
            : undefined;
    const valid = !portError && !fromError && !timeError && !urlError && !passwordError;
    const canSave = dirty && valid && !saving;
    const smtpSaved = dto.smtp_host !== '' && dto.smtp_from !== '';
    const testAddress = testTo.trim() || me?.user.email || '';
    const canTest = smtpSaved && !smtpDirty && testAddress !== '' && !testToError && !testing;
    const blocker = blockerOf(dto);
    const recipients = dto.recipients ?? [];
    const selectedWithEmail = recipients.filter(r => form.notify_user_ids.includes(r.id) && r.email).length;
    const selectedWithoutEmail = form.notify_user_ids.length - selectedWithEmail;

    const intervalLabel = (days: number) =>
        days === 1 ? t('value.notifyInterval.daily') : t('value.notifyInterval.days', { count: days });

    const saveButton = (
        <Button variant='primary' icon='save' loading={saving} disabled={!canSave} onClick={() => void save()}>
            {t('common.save')}
        </Button>
    );

    return (
        <PageContainer>
            <PageHeader
                title={t('layout.nav.settingsNotifications')}
                description={t('page.notifications.description')}
                crumbs={[
                    { label: t('layout.nav.settings'), to: '/settings' },
                    { label: t('layout.nav.settingsNotifications') },
                ]}
                actions={saveButton}
            />

            <div className='flex flex-col gap-6'>
                {blocker ? (
                    <Alert
                        tone={blocker === 'disabled' ? 'info' : 'warning'}
                        title={t('page.notifications.status.blockedTitle')}
                    >
                        <p>{t(`page.notifications.status.${blocker}`)}</p>
                        {dirty && <p className='mt-1'>{t('page.notifications.status.unsaved')}</p>}
                    </Alert>
                ) : (
                    <Alert tone='success' title={t('page.notifications.status.readyTitle')}>
                        <p>
                            {t('page.notifications.status.ready', {
                                time: dto.notify_time,
                                interval: intervalLabel(dto.notify_interval_days),
                            })}
                        </p>
                        {dirty && <p className='mt-1'>{t('page.notifications.status.unsaved')}</p>}
                    </Alert>
                )}

                <Card>
                    <CardHeader title={t('page.notifications.smtp')} description={t('page.notifications.smtpHint')} />
                    <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                        <InputField
                            label={t('field.notification.host')}
                            autoComplete='off'
                            value={form.smtp_host}
                            onChange={e => set('smtp_host', e.target.value)}
                            placeholder='smtp.example.com'
                        />
                        <div className='grid grid-cols-2 gap-4'>
                            <InputField
                                label={t('field.connection.port')}
                                type='number'
                                inputMode='numeric'
                                min={1}
                                max={65535}
                                step={1}
                                value={form.smtp_port}
                                onChange={e => set('smtp_port', e.target.value)}
                                error={portError}
                            />
                            <SelectField
                                label={t('field.connection.security')}
                                value={form.smtp_security}
                                onChange={e => set('smtp_security', e.target.value as SmtpSecurity)}
                            >
                                <option value='ssl'>{t('value.connectionSecurity.ssl')}</option>
                                <option value='starttls'>{t('value.connectionSecurity.starttls')}</option>
                                <option value='none'>{t('value.connectionSecurity.none')}</option>
                            </SelectField>
                        </div>
                        {form.smtp_security === 'none' && (
                            <div className='md:col-span-2'>
                                <Alert tone='warning'>{t('field.connection.securityNoneWarning')}</Alert>
                            </div>
                        )}
                        <InputField
                            label={t('field.connection.username')}
                            autoComplete='off'
                            value={form.smtp_username}
                            onChange={e => set('smtp_username', e.target.value)}
                            hint={t('field.notification.authHint')}
                        />
                        <div>
                            <InputField
                                label={
                                    <span className='inline-flex flex-wrap items-center gap-2'>
                                        {t('field.connection.password')}
                                        {dto.smtp_password_set && (
                                            <Badge tone='success' icon='check_circle'>
                                                {t('field.connection.passwordSet')}
                                            </Badge>
                                        )}
                                    </span>
                                }
                                type={showPassword ? 'text' : 'password'}
                                autoComplete='new-password'
                                value={form.smtp_password}
                                onChange={e => set('smtp_password', e.target.value)}
                                hint={
                                    dto.smtp_password_set
                                        ? t('field.connection.passwordKeepHint')
                                        : t('field.notification.authHint')
                                }
                                error={passwordError}
                            />
                            <CheckboxField
                                className='mt-1'
                                label={t('common.showPassword')}
                                checked={showPassword}
                                onChange={e => setShowPassword(e.target.checked)}
                            />
                        </div>
                        <InputField
                            label={t('field.notification.from')}
                            type='email'
                            autoComplete='off'
                            value={form.smtp_from}
                            onChange={e => set('smtp_from', e.target.value)}
                            placeholder='mailcare@example.com'
                            hint={t('field.notification.fromHint')}
                            error={fromError}
                            wrapperClassName='md:col-span-2'
                        />
                    </div>

                    <div className='mt-6 border-t border-line pt-4'>
                        <h3 className='text-base font-semibold text-ink'>{t('action.notification.test')}</h3>
                        <p className='mt-1 text-sm text-muted'>{t('page.notifications.testHint')}</p>
                        <div className='mt-3 flex flex-col gap-2 sm:flex-row sm:items-start'>
                            <InputField
                                label={t('field.notification.testTo')}
                                type='email'
                                autoComplete='off'
                                value={testTo}
                                onChange={e => setTestTo(e.target.value)}
                                placeholder={me?.user.email || undefined}
                                hint={
                                    me?.user.email
                                        ? t('field.notification.testToHint')
                                        : t('page.notifications.testNoAddress')
                                }
                                error={testToError}
                                wrapperClassName='flex-1'
                            />
                            <Button
                                icon='send'
                                loading={testing}
                                disabled={!canTest}
                                onClick={() => void test()}
                                className='sm:mt-6'
                            >
                                {t('action.notification.test')}
                            </Button>
                        </div>
                        {!smtpSaved && <Alert tone='info'>{t('page.notifications.testNeedsSmtp')}</Alert>}
                        {smtpSaved && smtpDirty && <Alert tone='info'>{t('page.notifications.testSaveFirst')}</Alert>}
                    </div>
                </Card>

                <Card>
                    <CardHeader
                        title={t('page.notifications.settings')}
                        description={t('page.notifications.settingsHint')}
                    />
                    <div className='flex flex-col gap-5'>
                        <ToggleField
                            label={t('field.notification.enabled')}
                            hint={t('field.notification.enabledHint')}
                            checked={form.notify_enabled}
                            onChange={v => set('notify_enabled', v)}
                        />
                        <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                            <InputField
                                label={t('field.notification.time')}
                                type='time'
                                step={60}
                                value={form.notify_time}
                                onChange={e => set('notify_time', e.target.value)}
                                hint={
                                    general.data?.server_timezone
                                        ? t('page.settingsGeneral.serverTimeZone', {
                                              zone: general.data.server_timezone,
                                          })
                                        : t('field.notification.timeHint')
                                }
                                error={timeError}
                            />
                            <SelectField
                                label={t('field.notification.interval')}
                                value={String(form.notify_interval_days)}
                                onChange={e => set('notify_interval_days', Number(e.target.value))}
                                hint={t('field.notification.intervalHint')}
                            >
                                {INTERVAL_OPTIONS.map(days => (
                                    <option key={days} value={String(days)}>
                                        {intervalLabel(days)}
                                    </option>
                                ))}
                            </SelectField>
                        </div>

                        <fieldset aria-describedby={`${recipientsId}-hint`}>
                            <legend className='text-sm font-medium text-ink'>
                                {t('field.notification.recipients')}
                            </legend>
                            <p id={`${recipientsId}-hint`} className='mt-1 text-sm text-muted'>
                                {t('field.notification.recipientsHint')}
                            </p>
                            {recipients.length === 0 ? (
                                <p className='mt-3 text-sm text-muted'>{t('page.notifications.noUsers')}</p>
                            ) : (
                                <ul className='mt-3 flex flex-col divide-y divide-line rounded-md border border-line'>
                                    {recipients.map(r => (
                                        <li key={r.id} className='px-3 py-1'>
                                            <CheckboxField
                                                label={
                                                    <span className='flex flex-wrap items-center gap-x-2 gap-y-1'>
                                                        <span className='font-medium'>
                                                            {r.display_name || r.username}
                                                        </span>
                                                        {r.display_name && (
                                                            <span className='text-sm text-muted'>{r.username}</span>
                                                        )}
                                                        {r.email ? (
                                                            <span className='break-all text-sm text-muted'>
                                                                {r.email}
                                                            </span>
                                                        ) : (
                                                            <Badge tone='warning' icon='warning'>
                                                                {t('page.notifications.noEmail')}
                                                            </Badge>
                                                        )}
                                                    </span>
                                                }
                                                checked={form.notify_user_ids.includes(r.id)}
                                                onChange={e => toggleRecipient(r.id, e.target.checked)}
                                            />
                                        </li>
                                    ))}
                                </ul>
                            )}
                            <p className='mt-2 text-sm text-muted'>
                                {t('page.notifications.recipientsSelected', { count: selectedWithEmail })}
                                {selectedWithoutEmail > 0 && (
                                    <span className='ml-2 text-warning'>
                                        {t('page.notifications.recipientsWithoutEmail', {
                                            count: selectedWithoutEmail,
                                        })}
                                    </span>
                                )}
                            </p>
                        </fieldset>

                        <InputField
                            label={t('field.notification.publicUrl')}
                            type='url'
                            inputMode='url'
                            autoComplete='off'
                            value={form.public_base_url}
                            onChange={e => set('public_base_url', e.target.value)}
                            placeholder='https://mailcare.example.com'
                            hint={
                                <>
                                    {t('field.notification.publicUrlHint')}
                                    <br />
                                    {t('page.notifications.publicUrlEffective')}{' '}
                                    <code className='break-all font-mono'>{dto.effective_base_url || '-'}</code>
                                </>
                            }
                            error={urlError}
                        />

                        <DescriptionList
                            items={[
                                {
                                    label: t('field.notification.lastSent'),
                                    value: (
                                        <DateTime
                                            value={dto.last_sent_at}
                                            relative
                                            empty={t('field.notification.lastSentNever')}
                                        />
                                    ),
                                },
                                {
                                    label: t('field.notification.nextSend'),
                                    value: (
                                        <DateTime
                                            value={dto.next_send_at}
                                            relative
                                            empty={t('field.notification.nextSendNone')}
                                        />
                                    ),
                                },
                            ]}
                        />

                        <div className='flex flex-col gap-2 border-t border-line pt-4'>
                            <div>
                                <Button
                                    icon='forward_to_inbox'
                                    loading={sending}
                                    disabled={blocker !== null || sending}
                                    onClick={() => void sendNow()}
                                >
                                    {t('action.notification.sendNow')}
                                </Button>
                            </div>
                            <p className='text-sm text-muted'>
                                {blocker ? t('page.notifications.sendBlocked') : t('page.notifications.sendNowHint')}
                            </p>
                        </div>
                    </div>
                </Card>

                <div className='flex justify-end'>{saveButton}</div>
            </div>
        </PageContainer>
    );
}
