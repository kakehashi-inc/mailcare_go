import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { ApiError, getProbeSettings, getSettings, testProbeServer, updateProbeSettings } from '../api/client';
import { Alert } from '../components/ui/Alert';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import {
    CheckboxField,
    CONTROL_WIDTH,
    InputField,
    SelectField,
    ToggleField,
    controlClass,
} from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useToast } from '../components/ui/Toast';
import {
    DEFAULT_FOLDER,
    DEFAULT_IMAP_PORT_PLAIN,
    DEFAULT_IMAP_PORT_SSL,
    DEFAULT_PROBE_DELAY_MINUTES,
    DEFAULT_PROBE_FAILURE_MINUTES,
    MAX_PROBE_MINUTES,
    MIN_PROBE_MINUTES,
} from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import type { ImapSecurity, ProbeSettingsDTO, ProbeSettingsInput } from '../types';
import { formatMinutes } from '../utils/format';
import { isClockTime, isEmailAddress, isIntegerInRange } from '../utils/validate';

interface FormState {
    enabled: boolean;
    address: string;
    imap_host: string;
    imap_port: string;
    imap_security: ImapSecurity;
    imap_username: string;
    /** Only sent when typed; an empty field keeps the stored password. */
    imap_password: string;
    imap_folder: string;
    send_times: string[];
    delay_minutes: string;
    failure_minutes: string;
}

/** The keys that name the server the stored password belongs to; changing one of them needs the password again. */
const CONNECTION_KEYS = ['imap_host', 'imap_port', 'imap_security', 'imap_username'] as const;

// Every field is defaulted: the server may omit empty optional fields.
function fromDto(dto: ProbeSettingsDTO): FormState {
    return {
        enabled: dto.enabled ?? false,
        address: dto.address ?? '',
        imap_host: dto.imap_host ?? '',
        imap_port: String(dto.imap_port || DEFAULT_IMAP_PORT_SSL),
        imap_security: dto.imap_security || 'ssl',
        imap_username: dto.imap_username ?? '',
        imap_password: '',
        imap_folder: dto.imap_folder || DEFAULT_FOLDER,
        send_times: [...(dto.send_times ?? [])],
        delay_minutes: String(dto.delay_minutes || DEFAULT_PROBE_DELAY_MINUTES),
        failure_minutes: String(dto.failure_minutes || DEFAULT_PROBE_FAILURE_MINUTES),
    };
}

/** The fields that differ from the saved settings; password only when typed. */
function diff(form: FormState, saved: FormState): ProbeSettingsInput {
    const out: ProbeSettingsInput = {};
    if (form.enabled !== saved.enabled) out.enabled = form.enabled;
    if (form.address.trim() !== saved.address) out.address = form.address.trim();
    if (form.imap_host.trim() !== saved.imap_host) out.imap_host = form.imap_host.trim();
    if (form.imap_port !== saved.imap_port) out.imap_port = Number(form.imap_port);
    if (form.imap_security !== saved.imap_security) out.imap_security = form.imap_security;
    if (form.imap_username.trim() !== saved.imap_username) out.imap_username = form.imap_username.trim();
    if (form.imap_password !== '') out.imap_password = form.imap_password;
    if ((form.imap_folder.trim() || DEFAULT_FOLDER) !== saved.imap_folder) {
        out.imap_folder = form.imap_folder.trim() || DEFAULT_FOLDER;
    }
    if (form.send_times.join(',') !== saved.send_times.join(',')) out.send_times = [...form.send_times];
    if (form.delay_minutes !== saved.delay_minutes) out.delay_minutes = Number(form.delay_minutes);
    if (form.failure_minutes !== saved.failure_minutes) out.failure_minutes = Number(form.failure_minutes);
    return out;
}

export function SettingsProbePage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.settingsProbe'));
    const toast = useToast();
    const settings = useAsync(getProbeSettings, []);
    const general = useAsync(getSettings, []);
    const [form, setForm] = useState<FormState | null>(null);
    const [saved, setSaved] = useState<FormState | null>(null);
    const [saving, setSaving] = useState(false);
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null);
    const [showPassword, setShowPassword] = useState(false);
    const [newTime, setNewTime] = useState('');

    useEffect(() => {
        if (!settings.data) return;
        const state = fromDto(settings.data);
        setForm(state);
        setSaved(state);
    }, [settings.data]);

    function set<K extends keyof FormState>(key: K, value: FormState[K]) {
        setForm(prev => (prev ? { ...prev, [key]: value } : prev));
        if ((CONNECTION_KEYS as readonly string[]).includes(key) || key === 'imap_password' || key === 'imap_folder') {
            setTestResult(null);
        }
    }

    function setSecurity(value: ImapSecurity) {
        setForm(prev => {
            if (!prev) return prev;
            const wasDefault =
                prev.imap_port === String(DEFAULT_IMAP_PORT_SSL) || prev.imap_port === String(DEFAULT_IMAP_PORT_PLAIN);
            const port = wasDefault
                ? String(value === 'ssl' ? DEFAULT_IMAP_PORT_SSL : DEFAULT_IMAP_PORT_PLAIN)
                : prev.imap_port;
            return { ...prev, imap_security: value, imap_port: port };
        });
        setTestResult(null);
    }

    function addTime() {
        if (!form || !isClockTime(newTime) || form.send_times.includes(newTime)) return;
        set('send_times', [...form.send_times, newTime].sort());
        setNewTime('');
    }

    async function save() {
        if (!form || !saved) return;
        setSaving(true);
        try {
            await updateProbeSettings(diff(form, saved));
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
        if (!form) return;
        setTesting(true);
        setTestResult(null);
        try {
            await testProbeServer({
                imap_host: form.imap_host.trim(),
                imap_port: Number(form.imap_port),
                imap_security: form.imap_security,
                imap_username: form.imap_username.trim(),
                imap_password: form.imap_password || undefined,
                imap_folder: form.imap_folder.trim() || DEFAULT_FOLDER,
            });
            setTestResult({ ok: true, message: t('page.probeSettings.testOk') });
        } catch (err) {
            setTestResult({ ok: false, message: t((err as ApiError).key, (err as ApiError).params) });
        } finally {
            setTesting(false);
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
    const portError = isIntegerInRange(form.imap_port, 1, 65535)
        ? undefined
        : t('validation.common.numberOutOfRange', { min: 1, max: 65535 });
    const addressError =
        form.address.trim() !== '' && !isEmailAddress(form.address) ? t('validation.common.emailFormat') : undefined;
    const minutesError = (v: string) =>
        isIntegerInRange(v, MIN_PROBE_MINUTES, MAX_PROBE_MINUTES)
            ? undefined
            : t('validation.common.numberOutOfRange', { min: MIN_PROBE_MINUTES, max: MAX_PROBE_MINUTES });
    const delayError = minutesError(form.delay_minutes);
    const failureError =
        minutesError(form.failure_minutes) ??
        (!delayError && Number(form.failure_minutes) <= Number(form.delay_minutes)
            ? t('validation.probe.failureNotAfterDelay')
            : undefined);
    // The server refuses to point a stored password at another server: a change of the connection settings must
    // carry the password.
    const connectionDirty = CONNECTION_KEYS.some(key => key in changes);
    const passwordError =
        connectionDirty && dto.imap_password_set && form.imap_password === ''
            ? t('validation.connection.passwordRequiredOnChange')
            : undefined;
    const passwordKnown = dto.imap_password_set || form.imap_password !== '';
    const configured =
        form.address.trim() !== '' && form.imap_host.trim() !== '' && form.imap_username.trim() !== '' && passwordKnown;
    const valid = !portError && !addressError && !delayError && !failureError && !passwordError;
    const canSave = dirty && valid && !saving && (!form.enabled || configured);
    const canTest =
        form.imap_host.trim() !== '' &&
        form.imap_username.trim() !== '' &&
        passwordKnown &&
        !portError &&
        !passwordError;
    const newTimeValid = newTime === '' || isClockTime(newTime);
    const newTimeDuplicate = newTime !== '' && form.send_times.includes(newTime);
    const savedConfigured =
        dto.address !== '' && dto.imap_host !== '' && dto.imap_username !== '' && dto.imap_password_set;
    const zoneHint = general.data?.server_timezone
        ? ` ${t('page.settingsGeneral.serverTimeZone', { zone: general.data.server_timezone })}`
        : '';

    const saveButton = (
        <Button variant='primary' icon='save' loading={saving} disabled={!canSave} onClick={() => void save()}>
            {t('common.save')}
        </Button>
    );

    return (
        <PageContainer>
            <PageHeader
                title={t('layout.nav.settingsProbe')}
                description={t('page.probeSettings.description')}
                crumbs={[
                    { label: t('layout.nav.settings'), to: '/settings' },
                    { label: t('layout.nav.settingsProbe') },
                ]}
                actions={saveButton}
            />

            <div className='flex flex-col gap-6'>
                {dto.enabled && savedConfigured ? (
                    <Alert tone='success' title={t('page.probeSettings.status.readyTitle')}>
                        <p>
                            {t('page.probeSettings.status.ready', {
                                times: dto.send_times.length > 0 ? dto.send_times.join(', ') : '-',
                            })}
                        </p>
                        <p className='mt-1'>
                            {dto.mailboxes > 0
                                ? t('page.probeSettings.mailboxesCount', { count: dto.mailboxes })
                                : t('page.probeSettings.noMailboxes')}
                        </p>
                        {dirty && <p className='mt-1'>{t('page.probeSettings.status.unsaved')}</p>}
                    </Alert>
                ) : (
                    <Alert
                        tone={savedConfigured ? 'info' : 'warning'}
                        title={t('page.probeSettings.status.blockedTitle')}
                    >
                        <p>
                            {savedConfigured
                                ? t('page.probeSettings.status.disabled')
                                : t('page.probeSettings.status.notConfigured')}
                        </p>
                        {dirty && <p className='mt-1'>{t('page.probeSettings.status.unsaved')}</p>}
                    </Alert>
                )}

                <Card>
                    <CardHeader
                        title={t('page.probeSettings.server')}
                        description={t('page.probeSettings.serverHint')}
                    />
                    <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                        <InputField
                            label={t('field.probe.address')}
                            type='email'
                            autoComplete='off'
                            value={form.address}
                            onChange={e => set('address', e.target.value)}
                            placeholder='probe@example.com'
                            hint={t('field.probe.addressHint')}
                            error={addressError}
                            wrapperClassName='md:col-span-2'
                        />
                        <InputField
                            label={t('field.connection.host')}
                            autoComplete='off'
                            value={form.imap_host}
                            onChange={e => set('imap_host', e.target.value)}
                            placeholder='imap.example.com'
                        />
                        <div className='grid grid-cols-2 gap-4'>
                            <SelectField
                                label={t('field.connection.security')}
                                value={form.imap_security}
                                onChange={e => setSecurity(e.target.value as ImapSecurity)}
                            >
                                <option value='ssl'>{t('value.connectionSecurity.ssl')}</option>
                                <option value='starttls'>{t('value.connectionSecurity.starttls')}</option>
                                <option value='none'>{t('value.connectionSecurity.none')}</option>
                            </SelectField>
                            <InputField
                                label={t('field.connection.port')}
                                type='number'
                                inputMode='numeric'
                                min={1}
                                max={65535}
                                step={1}
                                value={form.imap_port}
                                onChange={e => set('imap_port', e.target.value)}
                                error={portError}
                            />
                        </div>
                        {form.imap_security === 'none' && (
                            <div className='md:col-span-2'>
                                <Alert tone='warning'>{t('field.connection.securityNoneWarning')}</Alert>
                            </div>
                        )}
                        <InputField
                            label={t('field.connection.username')}
                            autoComplete='off'
                            value={form.imap_username}
                            onChange={e => set('imap_username', e.target.value)}
                        />
                        <div>
                            <InputField
                                label={
                                    <span className='inline-flex flex-wrap items-center gap-2'>
                                        {t('field.connection.password')}
                                        {dto.imap_password_set && (
                                            <Badge tone='success' icon='check_circle'>
                                                {t('field.connection.passwordSet')}
                                            </Badge>
                                        )}
                                    </span>
                                }
                                type={showPassword ? 'text' : 'password'}
                                autoComplete='new-password'
                                value={form.imap_password}
                                onChange={e => set('imap_password', e.target.value)}
                                hint={dto.imap_password_set ? t('field.connection.passwordKeepHint') : undefined}
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
                            label={t('field.common.folder')}
                            autoComplete='off'
                            value={form.imap_folder}
                            onChange={e => set('imap_folder', e.target.value)}
                            placeholder={DEFAULT_FOLDER}
                        />
                    </div>
                    <div className='mt-4 flex flex-col gap-3'>
                        <div>
                            <Button
                                icon='network_check'
                                loading={testing}
                                disabled={!canTest}
                                onClick={() => void test()}
                            >
                                {t('action.probe.test')}
                            </Button>
                        </div>
                        <p className='text-sm text-muted'>{t('page.probeSettings.testSaveFirst')}</p>
                        {testResult && <Alert tone={testResult.ok ? 'success' : 'danger'}>{testResult.message}</Alert>}
                    </div>
                </Card>

                <Card>
                    <CardHeader
                        title={t('page.probeSettings.schedule')}
                        description={t('page.probeSettings.scheduleHint')}
                    />
                    <div className='flex flex-col gap-5'>
                        <ToggleField
                            label={t('field.probe.enabled')}
                            hint={t('field.probe.enabledHint')}
                            checked={form.enabled}
                            onChange={v => set('enabled', v)}
                        />
                        {form.enabled && !configured && <Alert tone='warning'>{t('result.probe.notConfigured')}</Alert>}

                        <div>
                            <p className='text-sm font-medium text-ink'>{t('field.probe.sendTimes')}</p>
                            <p className='mt-1 text-sm text-muted'>
                                {t('field.probe.sendTimesHint')}
                                {zoneHint}
                            </p>
                            {form.send_times.length === 0 ? (
                                <p className='mt-3 text-sm text-muted'>{t('page.probeSettings.noSendTimes')}</p>
                            ) : (
                                <ul
                                    className={`mt-3 flex flex-col gap-2 ${CONTROL_WIDTH.short}`}
                                    aria-label={t('field.probe.sendTimes')}
                                >
                                    {form.send_times.map(time => (
                                        <li
                                            key={time}
                                            className='flex items-center justify-between gap-3 rounded-md border border-line px-3 py-1'
                                        >
                                            <span className='font-mono text-base text-ink'>{time}</span>
                                            <IconButton
                                                icon='delete'
                                                label={t('action.setting.removeTime', { time })}
                                                onClick={() =>
                                                    set(
                                                        'send_times',
                                                        form.send_times.filter(x => x !== time)
                                                    )
                                                }
                                            />
                                        </li>
                                    ))}
                                </ul>
                            )}
                            <form
                                className='mt-4'
                                onSubmit={e => {
                                    e.preventDefault();
                                    addTime();
                                }}
                            >
                                <label htmlFor='new-probe-time' className='mb-1 block text-sm font-medium text-ink'>
                                    {t('action.setting.addTime')}
                                </label>
                                <div className='flex items-center gap-2'>
                                    <input
                                        id='new-probe-time'
                                        type='time'
                                        step={60}
                                        value={newTime}
                                        onChange={e => setNewTime(e.target.value)}
                                        aria-invalid={!newTimeValid || newTimeDuplicate || undefined}
                                        aria-describedby='new-probe-time-hint'
                                        className={`${controlClass} min-w-0 ${CONTROL_WIDTH.short}`}
                                    />
                                    <Button
                                        type='submit'
                                        icon='add'
                                        disabled={!newTime || !newTimeValid || newTimeDuplicate}
                                        className='shrink-0'
                                    >
                                        {t('common.add')}
                                    </Button>
                                </div>
                                <p
                                    id='new-probe-time-hint'
                                    className={`mt-1 text-sm ${newTimeDuplicate ? 'text-danger' : 'text-muted'}`}
                                >
                                    {newTimeDuplicate
                                        ? t('validation.common.timeDuplicate')
                                        : t('page.probeSettings.addTimeHint')}
                                </p>
                            </form>
                        </div>

                        <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                            <InputField
                                label={t('field.probe.delayMinutes')}
                                type='number'
                                inputMode='numeric'
                                min={MIN_PROBE_MINUTES}
                                max={MAX_PROBE_MINUTES}
                                step={1}
                                value={form.delay_minutes}
                                onChange={e => set('delay_minutes', e.target.value)}
                                hint={`${t('field.probe.delayMinutesHint')}${delayError ? '' : ` (${formatMinutes(Number(form.delay_minutes), t)})`}`}
                                error={delayError}
                            />
                            <InputField
                                label={t('field.probe.failureMinutes')}
                                type='number'
                                inputMode='numeric'
                                min={MIN_PROBE_MINUTES}
                                max={MAX_PROBE_MINUTES}
                                step={1}
                                value={form.failure_minutes}
                                onChange={e => set('failure_minutes', e.target.value)}
                                hint={`${t('field.probe.failureMinutesHint')}${failureError ? '' : ` (${formatMinutes(Number(form.failure_minutes), t)})`}`}
                                error={failureError}
                            />
                        </div>

                        <div className='flex flex-col gap-2 border-t border-line pt-4 text-sm'>
                            <p className='font-medium text-ink'>{t('field.probe.mailboxes')}</p>
                            <p className='text-muted'>
                                {dto.mailboxes > 0
                                    ? t('page.probeSettings.mailboxesCount', { count: dto.mailboxes })
                                    : t('page.probeSettings.noMailboxes')}{' '}
                                <Link
                                    to='/settings/mailboxes'
                                    className='font-medium text-accent underline underline-offset-2'
                                >
                                    {t('layout.nav.settingsMailboxes')}
                                </Link>
                            </p>
                            <p className='text-muted'>
                                {t('page.probeSettings.notifyHint')}{' '}
                                <Link
                                    to='/settings/notifications'
                                    className='font-medium text-accent underline underline-offset-2'
                                >
                                    {t('layout.nav.settingsNotifications')}
                                </Link>
                            </p>
                        </div>
                    </div>
                </Card>

                <div className='flex justify-end'>{saveButton}</div>
            </div>
        </PageContainer>
    );
}
