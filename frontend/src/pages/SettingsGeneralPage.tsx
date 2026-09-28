import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { getSettings, updateSettings } from '../api/client';
import { Alert } from '../components/ui/Alert';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { DescriptionList } from '../components/ui/DescriptionList';
import { ErrorState } from '../components/ui/ErrorState';
import { CONTROL_WIDTH, InputField, SelectField, ToggleField, controlClass } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useToast } from '../components/ui/Toast';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { errorMessage } from '../utils/errors';

const TIME_RE = /^([01]\d|2[0-3]):[0-5]\d$/;
const WORKERS_MIN = 1;
const WORKERS_MAX = 16;
const KEEP_DAYS_MIN = 1;
const KEEP_DAYS_MAX = 365;
const MAIL_KEEP_DAYS_MIN = 1;
const MAIL_KEEP_DAYS_MAX = 3650;
// Same rule as agent.ValidateModel: starts with a letter or digit, no white space.
const MODEL_MAX = 100;
const MODEL_RE = /^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,99}$/;

export function SettingsGeneralPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.settingsGeneral'));
    const toast = useToast();
    const settings = useAsync(getSettings, []);
    const [times, setTimes] = useState<string[]>([]);
    const [newTime, setNewTime] = useState('');
    const [provider, setProvider] = useState('');
    const [model, setModel] = useState('');
    const [enabled, setEnabled] = useState(false);
    const [keepDays, setKeepDays] = useState('');
    const [mailKeepDays, setMailKeepDays] = useState('');
    const [workers, setWorkers] = useState('');
    const [saving, setSaving] = useState(false);
    const [dirty, setDirty] = useState(false);

    useEffect(() => {
        if (!settings.data) return;
        setTimes(settings.data.check_times);
        setProvider(settings.data.agent_provider);
        setModel(settings.data.agent_model);
        setEnabled(settings.data.agent_enabled);
        setKeepDays(String(settings.data.agent_keep_days));
        setMailKeepDays(String(settings.data.mail_keep_days));
        setWorkers(String(settings.data.workers ?? ''));
        setDirty(false);
    }, [settings.data]);

    const invalidTimes = times.filter(x => !TIME_RE.test(x));
    const duplicate = new Set(times).size !== times.length;
    const newTimeValid = newTime === '' || TIME_RE.test(newTime);
    const newTimeDuplicate = newTime !== '' && times.includes(newTime);
    const selectedProvider = settings.data?.providers.find(p => p.name === provider);
    const modelSupported = Boolean(selectedProvider?.model_option);
    const modelValid = model.trim() === '' || MODEL_RE.test(model.trim());
    const workersValue = Number(workers);
    const workersValid = /^\d+$/.test(workers.trim()) && workersValue >= WORKERS_MIN && workersValue <= WORKERS_MAX;
    const keepDaysValue = Number(keepDays);
    const keepDaysValid =
        /^\d+$/.test(keepDays.trim()) && keepDaysValue >= KEEP_DAYS_MIN && keepDaysValue <= KEEP_DAYS_MAX;
    const mailKeepDaysValue = Number(mailKeepDays);
    const mailKeepDaysValid =
        /^\d+$/.test(mailKeepDays.trim()) &&
        mailKeepDaysValue >= MAIL_KEEP_DAYS_MIN &&
        mailKeepDaysValue <= MAIL_KEEP_DAYS_MAX;
    const canSave =
        dirty &&
        invalidTimes.length === 0 &&
        !duplicate &&
        workersValid &&
        keepDaysValid &&
        mailKeepDaysValid &&
        modelValid &&
        !saving;

    function addTime() {
        if (!TIME_RE.test(newTime) || times.includes(newTime)) return;
        setTimes(prev => [...prev, newTime].sort());
        setNewTime('');
        setDirty(true);
    }

    function removeTime(index: number) {
        setTimes(prev => prev.filter((_, i) => i !== index));
        setDirty(true);
    }

    async function save() {
        setSaving(true);
        try {
            await updateSettings({
                check_times: times,
                agent_provider: provider,
                agent_model: modelSupported ? model.trim() : '',
                agent_enabled: enabled,
                agent_keep_days: keepDaysValue,
                mail_keep_days: mailKeepDaysValue,
                workers: workersValue,
            });
            toast.success(t('settings.saved'));
            await settings.reload();
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setSaving(false);
        }
    }

    if (settings.loading) return <LoadingBlock />;
    if (settings.error || !settings.data) {
        return (
            <PageContainer>
                <ErrorState message={errorMessage(settings.error, t)} onRetry={() => void settings.reload()} />
            </PageContainer>
        );
    }

    return (
        <PageContainer>
            <PageHeader
                title={t('nav.settingsGeneral')}
                crumbs={[{ label: t('nav.settings'), to: '/settings' }, { label: t('nav.settingsGeneral') }]}
                actions={
                    <Button
                        variant='primary'
                        icon='save'
                        loading={saving}
                        disabled={!canSave}
                        onClick={() => void save()}
                    >
                        {t('common.save')}
                    </Button>
                }
            />

            <div className='flex flex-col gap-6'>
                <Card>
                    <CardHeader
                        title={t('settings.checkTimes')}
                        description={`${t('settings.checkTimesHint')} ${t('settings.serverTimeZone', { zone: settings.data.server_timezone || '-' })}`}
                    />
                    {times.length === 0 ? (
                        <p className='text-sm text-muted'>{t('settings.noCheckTimes')}</p>
                    ) : (
                        <ul
                            className={`flex flex-col gap-2 ${CONTROL_WIDTH.short}`}
                            aria-label={t('settings.checkTimes')}
                        >
                            {times.map((time, i) => (
                                <li
                                    key={`${time}-${i}`}
                                    className='flex items-center justify-between gap-3 rounded-md border border-line px-3 py-1'
                                >
                                    <span
                                        className={`font-mono text-base ${TIME_RE.test(time) ? 'text-ink' : 'text-danger'}`}
                                    >
                                        {time}
                                    </span>
                                    <IconButton
                                        icon='delete'
                                        label={t('settings.removeTime', { time })}
                                        onClick={() => removeTime(i)}
                                    />
                                </li>
                            ))}
                        </ul>
                    )}
                    {(invalidTimes.length > 0 || duplicate) && (
                        <Alert tone='danger' className='mt-3'>
                            {invalidTimes.length > 0 ? t('settings.invalidTime') : t('settings.duplicateTime')}
                        </Alert>
                    )}
                    <form
                        className='mt-4'
                        onSubmit={e => {
                            e.preventDefault();
                            addTime();
                        }}
                    >
                        <label htmlFor='new-check-time' className='mb-1 block text-sm font-medium text-ink'>
                            {t('settings.addTime')}
                        </label>
                        <div className='flex items-center gap-2'>
                            <input
                                id='new-check-time'
                                type='time'
                                step={60}
                                value={newTime}
                                onChange={e => setNewTime(e.target.value)}
                                aria-invalid={!newTimeValid || newTimeDuplicate || undefined}
                                aria-describedby='new-check-time-hint'
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
                            id='new-check-time-hint'
                            className={`mt-1 text-sm ${newTimeDuplicate ? 'text-danger' : 'text-muted'}`}
                        >
                            {newTimeDuplicate ? t('settings.duplicateTime') : t('settings.addTimeHint')}
                        </p>
                    </form>
                </Card>

                <Card>
                    <CardHeader title={t('settings.mailRetention')} description={t('settings.mailRetentionHint')} />
                    <InputField
                        label={t('settings.mailKeepDays')}
                        type='number'
                        inputMode='numeric'
                        min={MAIL_KEEP_DAYS_MIN}
                        max={MAIL_KEEP_DAYS_MAX}
                        step={1}
                        value={mailKeepDays}
                        onChange={e => {
                            setMailKeepDays(e.target.value);
                            setDirty(true);
                        }}
                        hint={t('settings.mailKeepDaysHint')}
                        error={mailKeepDaysValid ? undefined : t('settings.mailKeepDaysInvalid')}
                        width='short'
                    />
                </Card>

                <Card>
                    <CardHeader title={t('settings.jobs')} description={t('settings.jobsHint')} />
                    <InputField
                        label={t('settings.workers')}
                        type='number'
                        inputMode='numeric'
                        min={WORKERS_MIN}
                        max={WORKERS_MAX}
                        step={1}
                        value={workers}
                        onChange={e => {
                            setWorkers(e.target.value);
                            setDirty(true);
                        }}
                        hint={t('settings.workersHint')}
                        error={workersValid ? undefined : t('settings.workersInvalid')}
                        width='short'
                    />
                </Card>

                <Card>
                    <CardHeader title={t('settings.agent')} description={t('settings.agentHint')} />
                    <div className='flex flex-col gap-4'>
                        <SelectField
                            label={t('settings.agentProvider')}
                            width='medium'
                            value={provider}
                            onChange={e => {
                                setProvider(e.target.value);
                                // Model names differ between providers (the server clears the model as well).
                                if (e.target.value !== settings.data?.agent_provider) setModel('');
                                else setModel(settings.data?.agent_model ?? '');
                                setDirty(true);
                            }}
                        >
                            {!settings.data.providers.some(p => p.name === provider) && (
                                <option value={provider}>{provider || '-'}</option>
                            )}
                            {settings.data.providers.map(p => (
                                <option key={p.name} value={p.name}>
                                    {p.label} {p.available ? '' : `(${t('settings.providerUnavailable')})`}
                                </option>
                            ))}
                        </SelectField>
                        <div className='flex flex-wrap items-center gap-2'>
                            <span className='text-sm text-muted'>{t('settings.providerStatus')}:</span>
                            {selectedProvider ? (
                                <Badge
                                    tone={selectedProvider.available ? 'success' : 'warning'}
                                    icon={selectedProvider.available ? 'check_circle' : 'warning'}
                                >
                                    {selectedProvider.available
                                        ? t('settings.providerAvailable')
                                        : t('settings.providerUnavailable')}
                                </Badge>
                            ) : (
                                <Badge tone='neutral' icon='help_outline'>
                                    {t('settings.providerUnknown')}
                                </Badge>
                            )}
                        </div>
                        {selectedProvider && !selectedProvider.available && (
                            <Alert tone='warning'>
                                {t('settings.providerUnavailableHint', { provider: selectedProvider.label })}
                            </Alert>
                        )}
                        <InputField
                            label={t('settings.agentModel')}
                            value={modelSupported ? model : ''}
                            onChange={e => {
                                setModel(e.target.value);
                                setDirty(true);
                            }}
                            disabled={!modelSupported}
                            placeholder={modelSupported ? t('settings.agentModelPlaceholder') : undefined}
                            autoComplete='off'
                            spellCheck={false}
                            maxLength={MODEL_MAX}
                            width='medium'
                            className='font-mono'
                            error={modelValid ? undefined : t('settings.agentModelInvalid')}
                            hint={
                                !modelSupported ? (
                                    t('settings.agentModelUnsupported')
                                ) : selectedProvider && selectedProvider.models.length > 0 ? (
                                    <span className='flex flex-wrap items-center gap-1'>
                                        <span>{t('settings.agentModelExamples')}:</span>
                                        {selectedProvider.models.map(m => (
                                            <button
                                                key={m}
                                                type='button'
                                                onClick={() => {
                                                    setModel(m);
                                                    setDirty(true);
                                                }}
                                                className='rounded bg-well px-1.5 py-0.5 font-mono text-xs text-ink hover:text-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                                            >
                                                {m}
                                            </button>
                                        ))}
                                    </span>
                                ) : undefined
                            }
                        />
                        <ToggleField
                            label={t('settings.agentEnabled')}
                            hint={t('settings.agentEnabledHint')}
                            checked={enabled}
                            onChange={v => {
                                setEnabled(v);
                                setDirty(true);
                            }}
                        />
                        <InputField
                            label={t('settings.agentKeepDays')}
                            type='number'
                            inputMode='numeric'
                            min={KEEP_DAYS_MIN}
                            max={KEEP_DAYS_MAX}
                            step={1}
                            value={keepDays}
                            onChange={e => {
                                setKeepDays(e.target.value);
                                setDirty(true);
                            }}
                            hint={t('settings.agentKeepDaysHint')}
                            error={keepDaysValid ? undefined : t('settings.agentKeepDaysInvalid')}
                            width='short'
                        />
                    </div>
                </Card>

                <Card>
                    <CardHeader title={t('settings.server')} description={t('settings.serverHint')} />
                    <DescriptionList
                        items={[
                            {
                                label: t('settings.webListen'),
                                value: <code className='font-mono'>{settings.data.web_listen || '-'}</code>,
                            },
                            {
                                label: t('settings.webPort'),
                                value: <code className='font-mono'>{settings.data.web_port ?? '-'}</code>,
                            },
                            {
                                label: t('settings.dataDir'),
                                value: <code className='break-all font-mono'>{settings.data.data_dir || '-'}</code>,
                                wide: true,
                            },
                        ]}
                    />
                </Card>

                <div className='flex justify-end'>
                    <Button
                        variant='primary'
                        icon='save'
                        loading={saving}
                        disabled={!canSave}
                        onClick={() => void save()}
                    >
                        {t('common.save')}
                    </Button>
                </div>
            </div>
        </PageContainer>
    );
}
