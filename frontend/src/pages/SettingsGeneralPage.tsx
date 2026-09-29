import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { getSettings, updateSettings, ApiError } from '../api/client';
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

/**
 * Reasoning levels offered for a model, with the same rule the server checks on save
 * (agent.CheckReasoningEffort): a listed model offers its own levels, no model offers
 * every known level, and a model the CLI does not list cannot be checked (known = false).
 */
function reasoningChoices(levels: Record<string, string[]>, model: string): { options: string[]; known: boolean } {
    const listed = model !== '' ? levels[model] : undefined;
    if (listed) return { options: listed, known: true };
    const all: string[] = [];
    for (const name of Object.keys(levels).sort()) {
        for (const level of levels[name]) if (!all.includes(level)) all.push(level);
    }
    return { options: all, known: model === '' && all.length > 0 };
}

export function SettingsGeneralPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.settingsGeneral'));
    const toast = useToast();
    const settings = useAsync(getSettings, []);
    const [times, setTimes] = useState<string[]>([]);
    const [newTime, setNewTime] = useState('');
    const [provider, setProvider] = useState('');
    const [model, setModel] = useState('');
    const [effort, setEffort] = useState('');
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
        setEffort(settings.data.agent_reasoning_effort);
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
    const reasoningSupported = Boolean(selectedProvider?.reasoning_option);
    const reasoning = reasoningChoices(selectedProvider?.reasoning_levels ?? {}, modelSupported ? model.trim() : '');
    // A level the chosen model does not accept blocks the save (the server refuses it as well).
    const effortValid = !reasoningSupported || effort === '' || !reasoning.known || reasoning.options.includes(effort);
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
        effortValid &&
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
                agent_reasoning_effort: reasoningSupported ? effort : '',
                agent_enabled: enabled,
                agent_keep_days: keepDaysValue,
                mail_keep_days: mailKeepDaysValue,
                workers: workersValue,
            });
            toast.success(t('result.setting.saved'));
            await settings.reload();
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setSaving(false);
        }
    }

    if (settings.loading) return <LoadingBlock />;
    if (settings.error || !settings.data) {
        return (
            <PageContainer>
                <ErrorState
                    message={t(settings.error?.key ?? 'system.internal', settings.error?.params)}
                    onRetry={() => void settings.reload()}
                />
            </PageContainer>
        );
    }

    return (
        <PageContainer>
            <PageHeader
                title={t('layout.nav.settingsGeneral')}
                crumbs={[
                    { label: t('layout.nav.settings'), to: '/settings' },
                    { label: t('layout.nav.settingsGeneral') },
                ]}
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
                        title={t('field.setting.checkTimes')}
                        description={`${t('field.setting.checkTimesHint')} ${t('page.settingsGeneral.serverTimeZone', { zone: settings.data.server_timezone || '-' })}`}
                    />
                    {times.length === 0 ? (
                        <p className='text-sm text-muted'>{t('page.settingsGeneral.noCheckTimes')}</p>
                    ) : (
                        <ul
                            className={`flex flex-col gap-2 ${CONTROL_WIDTH.short}`}
                            aria-label={t('field.setting.checkTimes')}
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
                                        label={t('action.setting.removeTime', { time })}
                                        onClick={() => removeTime(i)}
                                    />
                                </li>
                            ))}
                        </ul>
                    )}
                    {(invalidTimes.length > 0 || duplicate) && (
                        <Alert tone='danger' className='mt-3'>
                            {invalidTimes.length > 0
                                ? t('validation.common.timeFormat')
                                : t('validation.common.timeDuplicate')}
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
                            {t('action.setting.addTime')}
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
                            {newTimeDuplicate
                                ? t('validation.common.timeDuplicate')
                                : t('page.settingsGeneral.addTimeHint')}
                        </p>
                    </form>
                </Card>

                <Card>
                    <CardHeader
                        title={t('page.settingsGeneral.mailRetention')}
                        description={t('page.settingsGeneral.mailRetentionHint')}
                    />
                    <InputField
                        label={t('field.setting.mailKeepDays')}
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
                        hint={t('field.setting.mailKeepDaysHint')}
                        error={
                            mailKeepDaysValid
                                ? undefined
                                : t('validation.common.numberOutOfRange', {
                                      min: MAIL_KEEP_DAYS_MIN,
                                      max: MAIL_KEEP_DAYS_MAX,
                                  })
                        }
                        width='short'
                    />
                </Card>

                <Card>
                    <CardHeader
                        title={t('page.settingsGeneral.jobs')}
                        description={t('page.settingsGeneral.jobsHint')}
                    />
                    <InputField
                        label={t('field.setting.workers')}
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
                        hint={t('field.setting.workersHint')}
                        error={
                            workersValid
                                ? undefined
                                : t('validation.common.numberOutOfRange', { min: WORKERS_MIN, max: WORKERS_MAX })
                        }
                        width='short'
                    />
                </Card>

                <Card>
                    <CardHeader
                        title={t('page.settingsGeneral.agent')}
                        description={t('page.settingsGeneral.agentHint')}
                    />
                    <div className='flex flex-col gap-4'>
                        <SelectField
                            label={t('field.agent.provider')}
                            width='medium'
                            value={provider}
                            onChange={e => {
                                setProvider(e.target.value);
                                // Model names and reasoning levels differ between providers (the server
                                // clears them as well).
                                if (e.target.value !== settings.data?.agent_provider) {
                                    setModel('');
                                    setEffort('');
                                } else {
                                    setModel(settings.data?.agent_model ?? '');
                                    setEffort(settings.data?.agent_reasoning_effort ?? '');
                                }
                                setDirty(true);
                            }}
                        >
                            {!settings.data.providers.some(p => p.name === provider) && (
                                <option value={provider}>{provider || '-'}</option>
                            )}
                            {settings.data.providers.map(p => (
                                <option key={p.name} value={p.name}>
                                    {p.label} {p.available ? '' : `(${t('page.settingsGeneral.providerUnavailable')})`}
                                </option>
                            ))}
                        </SelectField>
                        <div className='flex flex-wrap items-center gap-2'>
                            <span className='text-sm text-muted'>{t('page.settingsGeneral.providerStatus')}:</span>
                            {selectedProvider ? (
                                <Badge
                                    tone={selectedProvider.available ? 'success' : 'warning'}
                                    icon={selectedProvider.available ? 'check_circle' : 'warning'}
                                >
                                    {selectedProvider.available
                                        ? t('page.settingsGeneral.providerAvailable')
                                        : t('page.settingsGeneral.providerUnavailable')}
                                </Badge>
                            ) : (
                                <Badge tone='neutral' icon='help_outline'>
                                    {t('page.settingsGeneral.providerUnknown')}
                                </Badge>
                            )}
                        </div>
                        {selectedProvider && !selectedProvider.available && (
                            <Alert tone='warning'>
                                {t('page.settingsGeneral.providerUnavailableHint', {
                                    provider: selectedProvider.label,
                                })}
                            </Alert>
                        )}
                        <InputField
                            label={t('field.agent.model')}
                            value={modelSupported ? model : ''}
                            onChange={e => {
                                setModel(e.target.value);
                                setDirty(true);
                            }}
                            disabled={!modelSupported}
                            placeholder={modelSupported ? t('field.setting.agentModelPlaceholder') : undefined}
                            autoComplete='off'
                            spellCheck={false}
                            maxLength={MODEL_MAX}
                            width='medium'
                            className='font-mono'
                            error={modelValid ? undefined : t('validation.agent.modelInvalid', { max: MODEL_MAX })}
                            hint={
                                !modelSupported ? (
                                    t('field.setting.agentModelUnsupported')
                                ) : selectedProvider && selectedProvider.models.length > 0 ? (
                                    <span className='flex flex-wrap items-center gap-1'>
                                        <span>{t('field.setting.agentModelExamples')}:</span>
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
                        <SelectField
                            label={t('field.agent.reasoning')}
                            width='medium'
                            value={reasoningSupported ? effort : ''}
                            onChange={e => {
                                setEffort(e.target.value);
                                setDirty(true);
                            }}
                            disabled={!reasoningSupported}
                            error={effortValid ? undefined : t('validation.agent.reasoningInvalid')}
                            hint={reasoningSupported ? undefined : t('field.setting.agentReasoningUnsupported')}
                        >
                            <option value=''>{t('field.setting.agentReasoningUnset')}</option>
                            {reasoningSupported && effort !== '' && !reasoning.options.includes(effort) && (
                                <option value={effort}>{effort}</option>
                            )}
                            {reasoningSupported &&
                                reasoning.options.map(level => (
                                    <option key={level} value={level}>
                                        {level}
                                    </option>
                                ))}
                        </SelectField>
                        <ToggleField
                            label={t('field.setting.agentEnabled')}
                            hint={t('field.setting.agentEnabledHint')}
                            checked={enabled}
                            onChange={v => {
                                setEnabled(v);
                                setDirty(true);
                            }}
                        />
                        <InputField
                            label={t('field.setting.agentKeepDays')}
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
                            hint={t('field.setting.agentKeepDaysHint')}
                            error={
                                keepDaysValid
                                    ? undefined
                                    : t('validation.common.numberOutOfRange', {
                                          min: KEEP_DAYS_MIN,
                                          max: KEEP_DAYS_MAX,
                                      })
                            }
                            width='short'
                        />
                    </div>
                </Card>

                <Card>
                    <CardHeader
                        title={t('page.settingsGeneral.server')}
                        description={t('page.settingsGeneral.serverHint')}
                    />
                    <DescriptionList
                        items={[
                            {
                                label: t('field.setting.webListen'),
                                value: <code className='font-mono'>{settings.data.web_listen || '-'}</code>,
                            },
                            {
                                label: t('field.setting.webPort'),
                                value: <code className='font-mono'>{settings.data.web_port ?? '-'}</code>,
                            },
                            {
                                label: t('field.setting.dataDir'),
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
