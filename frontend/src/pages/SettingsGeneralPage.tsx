import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { getSettings, updateSettings, ApiError } from '../api/client';
import { Alert } from '../components/ui/Alert';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { DescriptionList } from '../components/ui/DescriptionList';
import { ErrorState } from '../components/ui/ErrorState';
import {
    CONTROL_WIDTH,
    CheckboxField,
    InputField,
    SelectField,
    TextareaField,
    ToggleField,
    controlClass,
} from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useToast } from '../components/ui/Toast';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { BOUNCE_CATEGORIES } from '../types';
import {
    DNS_CHECK_RESOLVERS,
    MAX_DNS_CHECK_RESOLVERS,
    SENDING_SOURCE_HOSTS,
    SENDING_SOURCE_IPV4,
    SENDING_SOURCE_IPV6,
    checkList,
    listEntries,
    type ListError,
} from '../utils/addressList';
import { categoryLabel } from '../utils/category';

const TIME_RE = /^([01]\d|2[0-3]):[0-5]\d$/;
const WORKERS_MIN = 1;
const WORKERS_MAX = 16;
const KEEP_DAYS_MIN = 1;
const KEEP_DAYS_MAX = 365;
const MAIL_KEEP_DAYS_MIN = 1;
const MAIL_KEEP_DAYS_MAX = 3650;
const RECHECK_DAYS_MIN = 1;
const RECHECK_DAYS_MAX = 365;
// Same rule as agent.ValidateModel: starts with a letter or digit, no white space.
const MODEL_MAX = 100;
const MODEL_RE = /^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,99}$/;
// Attributes shared by the text areas that take one address or host name per line.
const LIST_TEXTAREA = {
    rows: 4,
    spellCheck: false,
    autoCapitalize: 'off',
    autoComplete: 'off',
    className: `resize-y font-mono ${CONTROL_WIDTH.medium}`,
} as const;

/**
 * Reasoning levels offered for a model, with the same rule the server checks on save
 * (agent.CheckReasoningEffort): a listed model offers the levels listed for it, no model offers
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
    const [cleanupTime, setCleanupTime] = useState('');
    // Re-check days per category, as typed.
    const [recheckDays, setRecheckDays] = useState<Record<string, string>>({});
    // Sending sources and resolvers as typed, one entry per line.
    const [sourceIpv4, setSourceIpv4] = useState('');
    const [sourceIpv6, setSourceIpv6] = useState('');
    const [sourceHosts, setSourceHosts] = useState('');
    const [dnsCheckEnabled, setDnsCheckEnabled] = useState(false);
    const [dnsResolvers, setDnsResolvers] = useState('');
    const [dnsSystemOnly, setDnsSystemOnly] = useState(false);
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
        setCleanupTime(settings.data.cleanup_time);
        setRecheckDays(
            Object.fromEntries(Object.entries(settings.data.recheck_days ?? {}).map(([c, n]) => [c, String(n)]))
        );
        setSourceIpv4((settings.data.sending_source_ipv4 ?? []).join('\n'));
        setSourceIpv6((settings.data.sending_source_ipv6 ?? []).join('\n'));
        setSourceHosts((settings.data.sending_source_hosts ?? []).join('\n'));
        setDnsCheckEnabled(settings.data.dns_check_enabled);
        setDnsResolvers((settings.data.dns_check_resolvers ?? []).join('\n'));
        setDnsSystemOnly(settings.data.dns_check_system_resolver_only);
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
    const cleanupTimeValid = TIME_RE.test(cleanupTime);
    const recheckDaysValid = (value: string) =>
        /^\d+$/.test(value.trim()) && Number(value) >= RECHECK_DAYS_MIN && Number(value) <= RECHECK_DAYS_MAX;
    const allRecheckDaysValid = Object.values(recheckDays).every(recheckDaysValid);
    const sourceIpv4Error = checkList(sourceIpv4, SENDING_SOURCE_IPV4);
    const sourceIpv6Error = checkList(sourceIpv6, SENDING_SOURCE_IPV6);
    const sourceHostsError = checkList(sourceHosts, SENDING_SOURCE_HOSTS);
    // The SMTP hosts of the mail addresses count as source host names too (read-only here).
    const mailboxHosts = settings.data?.sending_source_mailbox_hosts ?? [];
    const sendingSourceEmpty =
        [sourceIpv4, sourceIpv6, sourceHosts].every(text => listEntries(text).length === 0) &&
        mailboxHosts.length === 0;
    // The addresses without an SMTP host while no host name is saved here (derived by the server): the judgement
    // of notices about mail not sent from the sending sources is off for them. A host name typed here counts for
    // every address, so the warning goes away before it is saved.
    const withoutHost = settings.data?.sending_source_mailboxes_without_host ?? [];
    const sendingSourceNoHost = withoutHost.length > 0 && listEntries(sourceHosts).length === 0;
    // With no IP address listed here either, nothing at all is configured for these addresses: the DNS judgement
    // takes every reporting server as a sending server for them, so any entry typed here hides the sentence as well.
    const sendingSourceNoHostNoAddress =
        sendingSourceNoHost && listEntries(sourceIpv4).length === 0 && listEntries(sourceIpv6).length === 0;
    // The warning about the addresses already says all that the notice about an empty configuration says, so only
    // one of the two is shown (the notice remains for no address at all, or for a host name cleared here).
    const showSendingSourceEmpty = sendingSourceEmpty && !sendingSourceNoHost;
    // The resolvers are sent (and checked by the server) even while their field is disabled.
    const dnsResolversError = checkList(dnsResolvers, DNS_CHECK_RESOLVERS);
    // Same rule as modules.ValidateDNSCheck: the check needs a resolver unless it asks the OS resolver only.
    const dnsNoResolver = dnsCheckEnabled && !dnsSystemOnly && listEntries(dnsResolvers).length === 0;
    const listError = (err: ListError | null) => (err ? t(err.key, err.params) : undefined);
    const canSave =
        dirty &&
        invalidTimes.length === 0 &&
        !duplicate &&
        workersValid &&
        keepDaysValid &&
        mailKeepDaysValid &&
        cleanupTimeValid &&
        allRecheckDaysValid &&
        !sourceIpv4Error &&
        !sourceIpv6Error &&
        !sourceHostsError &&
        !dnsResolversError &&
        !dnsNoResolver &&
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
                cleanup_time: cleanupTime,
                recheck_days: Object.fromEntries(Object.entries(recheckDays).map(([c, v]) => [c, Number(v)])),
                sending_source_ipv4: listEntries(sourceIpv4),
                sending_source_ipv6: listEntries(sourceIpv6),
                sending_source_hosts: listEntries(sourceHosts),
                dns_check_enabled: dnsCheckEnabled,
                dns_check_resolvers: listEntries(dnsResolvers),
                dns_check_system_resolver_only: dnsSystemOnly,
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
                        title={t('page.settingsGeneral.cleanup')}
                        description={`${t('page.settingsGeneral.cleanupHint')} ${t('page.settingsGeneral.serverTimeZone', { zone: settings.data.server_timezone || '-' })}`}
                    />
                    <div className='flex flex-col gap-4'>
                        <InputField
                            label={t('field.setting.cleanupTime')}
                            type='time'
                            step={60}
                            value={cleanupTime}
                            onChange={e => {
                                setCleanupTime(e.target.value);
                                setDirty(true);
                            }}
                            hint={t('field.setting.cleanupTimeHint')}
                            error={cleanupTimeValid ? undefined : t('validation.common.timeFormat')}
                            width='short'
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
                    </div>
                </Card>

                <Card>
                    <CardHeader
                        title={t('page.settingsGeneral.recheck')}
                        description={t('page.settingsGeneral.recheckHint')}
                    />
                    <fieldset>
                        <legend className='text-sm font-medium text-ink'>{t('field.setting.recheckDays')}</legend>
                        <p className='mb-3 mt-1 text-sm text-muted'>{t('field.setting.recheckDaysHint')}</p>
                        <div className='grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2'>
                            {BOUNCE_CATEGORIES.filter(c => c in recheckDays).map(c => (
                                <InputField
                                    key={c}
                                    label={categoryLabel(c, t)}
                                    type='number'
                                    inputMode='numeric'
                                    min={RECHECK_DAYS_MIN}
                                    max={RECHECK_DAYS_MAX}
                                    step={1}
                                    value={recheckDays[c]}
                                    onChange={e => {
                                        const value = e.target.value;
                                        setRecheckDays(prev => ({ ...prev, [c]: value }));
                                        setDirty(true);
                                    }}
                                    error={
                                        recheckDaysValid(recheckDays[c])
                                            ? undefined
                                            : t('validation.common.numberOutOfRange', {
                                                  min: RECHECK_DAYS_MIN,
                                                  max: RECHECK_DAYS_MAX,
                                              })
                                    }
                                    width='short'
                                />
                            ))}
                        </div>
                    </fieldset>
                </Card>

                <Card>
                    <CardHeader
                        title={t('page.settingsGeneral.dnsCheck')}
                        description={t('page.settingsGeneral.dnsCheckHint')}
                    />
                    <fieldset aria-describedby='sending-source-hint'>
                        <legend className='text-sm font-medium text-ink'>{t('field.setting.sendingSource')}</legend>
                        <p id='sending-source-hint' className='mb-3 mt-1 text-sm text-muted'>
                            {t('field.setting.sendingSourceHint')}
                        </p>
                        <div className='flex flex-col gap-4'>
                            <TextareaField
                                {...LIST_TEXTAREA}
                                label={t('field.setting.sendingSourceIpv4')}
                                hint={t('field.setting.sendingSourceIpv4Hint')}
                                placeholder={t('field.setting.sendingSourceIpv4Placeholder')}
                                value={sourceIpv4}
                                onChange={e => {
                                    setSourceIpv4(e.target.value);
                                    setDirty(true);
                                }}
                                error={listError(sourceIpv4Error)}
                            />
                            <TextareaField
                                {...LIST_TEXTAREA}
                                label={t('field.setting.sendingSourceIpv6')}
                                hint={t('field.setting.sendingSourceIpv6Hint')}
                                placeholder={t('field.setting.sendingSourceIpv6Placeholder')}
                                value={sourceIpv6}
                                onChange={e => {
                                    setSourceIpv6(e.target.value);
                                    setDirty(true);
                                }}
                                error={listError(sourceIpv6Error)}
                            />
                            <TextareaField
                                {...LIST_TEXTAREA}
                                label={t('field.setting.sendingSourceHosts')}
                                hint={t('field.setting.sendingSourceHostsHint')}
                                placeholder={t('field.setting.sendingSourceHostsPlaceholder')}
                                value={sourceHosts}
                                onChange={e => {
                                    setSourceHosts(e.target.value);
                                    setDirty(true);
                                }}
                                error={listError(sourceHostsError)}
                            />
                            {mailboxHosts.length > 0 && (
                                <div className='text-sm'>
                                    <p className='font-medium text-ink'>
                                        {t('field.setting.sendingSourceMailboxHosts')}
                                    </p>
                                    <ul className='mt-1 flex flex-wrap gap-x-3 gap-y-1'>
                                        {mailboxHosts.map(host => (
                                            <li key={host} className='break-all font-mono text-ink'>
                                                {host}
                                            </li>
                                        ))}
                                    </ul>
                                    <p className='mt-1 text-muted'>
                                        {t('field.setting.sendingSourceMailboxHostsHint')}
                                    </p>
                                </div>
                            )}
                            {showSendingSourceEmpty && (
                                <Alert tone='info'>{t('page.settingsGeneral.sendingSourceEmpty')}</Alert>
                            )}
                            {sendingSourceNoHost && (
                                <Alert tone='warning' title={t('page.settingsGeneral.sendingSourceNoHost')}>
                                    <ul className='flex flex-wrap gap-x-3 gap-y-1'>
                                        {withoutHost.map(address => (
                                            <li key={address} className='break-all font-mono'>
                                                {address}
                                            </li>
                                        ))}
                                    </ul>
                                    {/* After the list, which the sentences refer back to ("these addresses"). */}
                                    <p className='mt-2'>{t('page.settingsGeneral.sendingSourceNoHostAction')}</p>
                                    {sendingSourceNoHostNoAddress && (
                                        <p>{t('page.settingsGeneral.sendingSourceNoHostNoAddress')}</p>
                                    )}
                                </Alert>
                            )}
                        </div>
                    </fieldset>
                    <div className='mt-6 flex flex-col gap-4 border-t border-line pt-4'>
                        <ToggleField
                            label={t('field.setting.dnsCheckEnabled')}
                            hint={t('field.setting.dnsCheckEnabledHint')}
                            checked={dnsCheckEnabled}
                            onChange={v => {
                                setDnsCheckEnabled(v);
                                setDirty(true);
                            }}
                        />
                        {/* Disabled controls keep their values, which are saved unchanged. */}
                        <TextareaField
                            {...LIST_TEXTAREA}
                            label={t('field.setting.dnsCheckResolvers')}
                            hint={t('field.setting.dnsCheckResolversHint', { max: MAX_DNS_CHECK_RESOLVERS })}
                            value={dnsResolvers}
                            onChange={e => {
                                setDnsResolvers(e.target.value);
                                setDirty(true);
                            }}
                            disabled={!dnsCheckEnabled || dnsSystemOnly}
                            error={
                                listError(dnsResolversError) ??
                                (dnsNoResolver ? t('validation.dnsCheck.noResolver') : undefined)
                            }
                        />
                        <CheckboxField
                            label={t('field.setting.dnsCheckSystemResolverOnly')}
                            hint={t('field.setting.dnsCheckSystemResolverOnlyHint')}
                            checked={dnsSystemOnly}
                            onChange={e => {
                                setDnsSystemOnly(e.target.checked);
                                setDirty(true);
                            }}
                            disabled={!dnsCheckEnabled}
                        />
                    </div>
                </Card>

                <Card>
                    <CardHeader title={t('page.settingsGeneral.jobs')} />
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
