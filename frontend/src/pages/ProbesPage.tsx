import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import { ApiError, checkProbesNow, listProbes, resolveProbe, sendProbesNow } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { ProbeBadges, ProbeDelay } from '../components/domain/ProbeBadges';
import { ProbeResolveDialog } from '../components/domain/ProbeResolveDialog';
import { Alert } from '../components/ui/Alert';
import { Button, LinkButton } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { DateTime } from '../components/ui/DateTime';
import { DescriptionList } from '../components/ui/DescriptionList';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { Pagination } from '../components/ui/Pagination';
import { LoadingBlock } from '../components/ui/Spinner';
import { Table, type Column } from '../components/ui/Table';
import { Tabs } from '../components/ui/Tabs';
import { useToast } from '../components/ui/Toast';
import { DASHBOARD_REFRESH_MS, PROBE_PAGE_SIZE } from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { usePolling } from '../hooks/usePolling';
import { PROBE_FILTERS, type JobSubmitResult, type ProbeFilter, type ProbeMailDTO, type ProbeSummary } from '../types';
import { formatDateTime, formatMinutes, formatNumber } from '../utils/format';

const FILTER_ICONS: Record<ProbeFilter, string> = {
    open: 'pending_actions',
    issues: 'history',
    all: 'list',
};

/** The send time of a probe mail as its subject names it (server time), marked when it was sent by hand. */
function slotLabel(p: ProbeMailDTO, manual: string): string {
    return p.manual ? `${p.slot} (${manual})` : p.slot;
}

/**
 * Delivery probe: the state of the probe mails sent from every mail address to the probe server. Every user sees
 * the counts, the newest probe mail of each address and the list (URL `filter` and `page`) and can resolve an open
 * probe mail with a reason, like the state of an alert; administrators can also send or check now.
 */
export function ProbesPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.probes'));
    const { isAdmin } = useAuth();
    const toast = useToast();
    const [params, setParams] = useSearchParams();
    const filterParam = params.get('filter');
    const filter: ProbeFilter = PROBE_FILTERS.find(f => f === filterParam) ?? 'open';
    const page = Math.max(1, Number(params.get('page')) || 1);
    const { data, error, loading, reload } = useAsync(
        () => listProbes({ filter, page, per_page: PROBE_PAGE_SIZE }),
        [filter, page]
    );
    const [resolving, setResolving] = useState<ProbeMailDTO | null>(null);
    const [busy, setBusy] = useState(false);
    const [acting, setActing] = useState<'send' | 'check' | null>(null);
    usePolling(reload, !loading, DASHBOARD_REFRESH_MS);

    function setFilter(next: ProbeFilter) {
        const sp = new URLSearchParams(params);
        if (next === 'open') sp.delete('filter');
        else sp.set('filter', next);
        sp.delete('page');
        setParams(sp, { replace: true });
    }

    function setPage(next: number) {
        const sp = new URLSearchParams(params);
        if (next <= 1) sp.delete('page');
        else sp.set('page', String(next));
        setParams(sp, { replace: true });
    }

    async function act(kind: 'send' | 'check') {
        setActing(kind);
        try {
            const r: JobSubmitResult = kind === 'send' ? await sendProbesNow() : await checkProbesNow();
            if (r.created) toast.success(t(kind === 'send' ? 'result.probe.sendQueued' : 'result.probe.checkQueued'));
            else toast.info(t('result.probe.alreadyQueued'));
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setActing(null);
        }
    }

    async function confirmResolve(reason: string, note: string) {
        if (!resolving) return;
        setBusy(true);
        try {
            await resolveProbe(resolving.id, reason, note);
            toast.success(t('result.probe.resolved'));
            setResolving(null);
            await reload();
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
            // The mail may have arrived meanwhile: show the current state.
            await reload();
        } finally {
            setBusy(false);
        }
    }

    const manual = t('page.probes.manual');
    const summary = data?.summary;
    const canAct = isAdmin && !!summary?.configured;

    const columns: Column<ProbeMailDTO>[] = [
        {
            key: 'slot',
            header: t('field.probe.slot'),
            primary: true,
            cell: p => <span className='font-mono text-sm'>{slotLabel(p, manual)}</span>,
        },
        {
            key: 'address',
            header: t('field.probe.mailbox'),
            cell: p => <span className='break-all'>{p.address}</span>,
        },
        { key: 'status', header: t('field.probe.status'), cell: p => <ProbeBadges probe={p} /> },
        { key: 'delay', header: t('field.probe.delay'), cell: p => <ProbeDelay probe={p} /> },
        {
            key: 'arrived',
            header: t('field.probe.arrivedAt'),
            cell: p => <DateTime value={p.arrived_at} empty='-' />,
        },
        {
            key: 'checked',
            header: t('field.probe.lastChecked'),
            cell: p => <DateTime value={p.last_checked_at} relative empty='-' />,
        },
        {
            key: 'detail',
            header: t('field.probe.detail'),
            className: 'max-w-xs',
            cell: p => <ProbeDetail probe={p} />,
        },
        {
            key: 'actions',
            header: <span className='sr-only'>{t('common.actions')}</span>,
            hideLabelInCard: true,
            cell: p =>
                p.status === 'pending' ||
                p.status === 'delayed' ||
                p.status === 'failure' ||
                p.status === 'send_failed' ? (
                    <Button size='sm' icon='task_alt' onClick={() => setResolving(p)}>
                        {t('action.probe.resolve')}
                    </Button>
                ) : null,
        },
    ];

    const actions = isAdmin ? (
        <div className='flex flex-wrap gap-2'>
            <Button
                icon='send'
                loading={acting === 'send'}
                disabled={!canAct || acting !== null}
                onClick={() => void act('send')}
            >
                {t('action.probe.sendNow')}
            </Button>
            <Button
                icon='mark_email_read'
                loading={acting === 'check'}
                disabled={!canAct || acting !== null}
                onClick={() => void act('check')}
            >
                {t('action.probe.checkNow')}
            </Button>
            <LinkButton to='/settings/probe' icon='tune'>
                {t('layout.nav.settingsProbe')}
            </LinkButton>
        </div>
    ) : undefined;

    return (
        <PageContainer wide>
            <PageHeader
                title={t('layout.nav.probes')}
                description={t('page.probes.description')}
                crumbs={[{ label: t('layout.nav.alerts'), to: '/alerts' }, { label: t('layout.nav.probes') }]}
                actions={actions}
            />
            {loading && !data ? (
                <LoadingBlock />
            ) : error || !data || !summary ? (
                <ErrorState message={t(error?.key ?? 'system.internal', error?.params)} onRetry={() => void reload()} />
            ) : (
                <div className='flex flex-col gap-6'>
                    <ProbeNotices summary={summary} isAdmin={isAdmin} />
                    <ProbeCounts summary={summary} />

                    <div className='grid grid-cols-1 items-start gap-6 lg:grid-cols-3'>
                        <Card className='lg:col-span-2'>
                            <CardHeader title={t('page.probes.mailboxes')} />
                            {summary.mailboxes.length === 0 ? (
                                <p className='text-sm text-muted'>{t('page.probeSettings.noMailboxes')}</p>
                            ) : (
                                <ul className='flex flex-col divide-y divide-line'>
                                    {summary.mailboxes.map(mb => (
                                        <li
                                            key={mb.mailbox_id}
                                            className='flex flex-col gap-1 py-2 sm:flex-row sm:items-center sm:justify-between sm:gap-3'
                                        >
                                            <div className='min-w-0'>
                                                <p className='truncate font-medium text-ink'>
                                                    {mb.display_name || mb.address}
                                                </p>
                                                {mb.display_name && (
                                                    <p className='truncate text-sm text-muted'>{mb.address}</p>
                                                )}
                                                {mb.latest && (
                                                    <p className='text-sm text-muted'>
                                                        <span className='font-mono'>
                                                            {slotLabel(mb.latest, manual)}
                                                        </span>{' '}
                                                        {mb.latest.status !== 'send_failed' &&
                                                            (mb.latest.arrived_at !== null ||
                                                                mb.latest.delay_seconds > 0) && (
                                                                <ProbeDelay probe={mb.latest} />
                                                            )}
                                                    </p>
                                                )}
                                            </div>
                                            <div className='flex shrink-0 flex-wrap items-center gap-1'>
                                                {mb.latest ? (
                                                    <ProbeBadges probe={mb.latest} />
                                                ) : (
                                                    <span className='text-sm text-muted'>
                                                        {t('page.probes.neverSent')}
                                                    </span>
                                                )}
                                                {!mb.sending && (
                                                    <span className='text-sm text-muted'>
                                                        ({t('page.probes.notSending')})
                                                    </span>
                                                )}
                                            </div>
                                        </li>
                                    ))}
                                </ul>
                            )}
                        </Card>
                        <Card>
                            <CardHeader title={t('page.dashboard.schedule')} as='h2' />
                            <ProbeSchedule summary={summary} />
                        </Card>
                    </div>

                    <section>
                        <Tabs
                            label={t('page.probes.list')}
                            value={filter}
                            onChange={setFilter}
                            tabs={PROBE_FILTERS.map(f => ({
                                key: f,
                                label: t(`value.probeFilter.${f}`),
                                icon: FILTER_ICONS[f],
                                count: f === filter ? data.total : undefined,
                            }))}
                        />
                        <div className='mt-4 flex flex-col gap-4'>
                            <Table
                                columns={columns}
                                rows={data.probes}
                                rowKey={p => p.id}
                                caption={t('page.probes.list')}
                                emptyState={<EmptyState title={t(`page.probes.empty.${filter}`)} />}
                            />
                            {data.total > data.per_page && (
                                <Pagination
                                    page={data.page}
                                    perPage={data.per_page}
                                    total={data.total}
                                    onChange={setPage}
                                />
                            )}
                        </div>
                    </section>
                </div>
            )}

            <ProbeResolveDialog
                open={resolving !== null}
                subject={
                    resolving
                        ? t('page.probes.resolveFor', {
                              address: resolving.address,
                              slot: slotLabel(resolving, manual),
                          })
                        : ''
                }
                busy={busy}
                onConfirm={(reason, note) => void confirmResolve(reason, note)}
                onCancel={() => setResolving(null)}
            />
        </PageContainer>
    );
}

/** Why a probe mail failed to be sent, or how it was resolved. */
function ProbeDetail({ probe }: { probe: ProbeMailDTO }) {
    const { t } = useTranslation();
    if (probe.status === 'resolved' && probe.resolve_reason) {
        return (
            <div className='flex flex-col gap-0.5 text-sm'>
                <span className='text-ink'>{t(`value.probeResolveReason.${probe.resolve_reason}`)}</span>
                {probe.resolve_note && <span className='break-words text-muted'>{probe.resolve_note}</span>}
                {probe.resolved_at && (
                    <span className='text-muted'>
                        {t('field.probe.resolvedBy', {
                            user: probe.resolved_by || '-',
                            date: formatDateTime(probe.resolved_at),
                        })}
                    </span>
                )}
            </div>
        );
    }
    if (probe.error_message) {
        return <span className='break-words text-sm text-danger'>{probe.error_message}</span>;
    }
    return <span className='text-muted'>-</span>;
}

/** Notices about the probe itself: switched off, not set up, or the latest check failed. */
function ProbeNotices({ summary, isAdmin }: { summary: ProbeSummary; isAdmin: boolean }) {
    const { t } = useTranslation();
    if (!summary.configured) {
        return (
            <Alert
                tone='info'
                title={t('page.probes.notConfiguredTitle')}
                actions={
                    isAdmin ? (
                        <LinkButton size='sm' to='/settings/probe' icon='tune'>
                            {t('layout.nav.settingsProbe')}
                        </LinkButton>
                    ) : undefined
                }
            >
                {t('page.probes.notConfigured')}
            </Alert>
        );
    }
    return (
        <>
            {!summary.enabled && (
                <Alert tone='info' title={t('page.probes.disabledTitle')}>
                    {t('page.probes.disabled')}
                </Alert>
            )}
            {summary.last_check_error && (
                <Alert tone='warning' title={t('page.probes.checkErrorTitle')}>
                    {isAdmin
                        ? t('page.probes.checkError', { error: summary.last_check_error })
                        : t('page.probes.checkErrorMember')}
                </Alert>
            )}
        </>
    );
}

/** The counters of the open probe mails and of the recently recovered and resolved ones. */
export function ProbeCounts({ summary }: { summary: ProbeSummary }) {
    const { t } = useTranslation();
    const c = summary.counts;
    const items = [
        { key: 'failure', value: c.failure, icon: 'error', tone: c.failure > 0 ? 'text-danger' : 'text-ink' },
        {
            key: 'send_failed',
            value: c.send_failed,
            icon: 'cancel_schedule_send',
            tone: c.send_failed > 0 ? 'text-danger' : 'text-ink',
        },
        {
            key: 'delayed',
            value: c.delayed,
            icon: 'hourglass_bottom',
            tone: c.delayed > 0 ? 'text-warning' : 'text-ink',
        },
        { key: 'pending', value: c.pending, icon: 'schedule_send', tone: 'text-ink' },
        { key: 'recovered', value: c.recovered, icon: 'restore', tone: 'text-ink' },
        { key: 'resolved', value: c.resolved, icon: 'task_alt', tone: 'text-ink' },
    ] as const;
    return (
        <section aria-label={t('page.probes.counts')}>
            <ul className='grid grid-cols-2 gap-3 md:grid-cols-3 lg:grid-cols-6'>
                {items.map(item => (
                    <li key={item.key}>
                        <Card className='flex h-full items-center gap-3'>
                            <Icon name={item.icon} className='text-[28px] text-muted' />
                            <div className='min-w-0'>
                                <p className='break-words text-sm leading-tight text-muted'>
                                    {t(`value.probeStatus.${item.key}`)}
                                </p>
                                <p className={`text-2xl font-bold ${item.tone}`}>{formatNumber(item.value)}</p>
                            </div>
                        </Card>
                    </li>
                ))}
            </ul>
            <p className='mt-2 text-sm text-muted'>{t('page.probes.recentHint', { days: summary.recent_days })}</p>
        </section>
    );
}

/** Send times, thresholds and the times of the last and next send and check. */
export function ProbeSchedule({ summary }: { summary: ProbeSummary }) {
    const { t } = useTranslation();
    return (
        <div className='flex flex-col gap-4'>
            <div>
                <p className='text-sm text-muted'>{t('field.probe.sendTimes')}</p>
                <div className='mt-1 flex flex-wrap gap-2'>
                    {summary.send_times.length === 0 ? (
                        <span className='text-muted'>{t('page.dashboard.noSchedule')}</span>
                    ) : (
                        summary.send_times.map(time => (
                            <span key={time} className='rounded-md bg-well px-2 py-0.5 font-mono text-sm text-ink'>
                                {time}
                            </span>
                        ))
                    )}
                </div>
            </div>
            <DescriptionList
                columns={1}
                items={[
                    {
                        label: t('field.probe.thresholds'),
                        value: t('field.probe.thresholdsValue', {
                            delay: formatMinutes(summary.delay_minutes, t),
                            failure: formatMinutes(summary.failure_minutes, t),
                        }),
                    },
                    {
                        label: t('field.probe.lastSend'),
                        value: <DateTime value={summary.last_send_at} relative empty='-' />,
                    },
                    {
                        label: t('field.probe.nextSend'),
                        value: <DateTime value={summary.next_send_at} relative empty='-' />,
                    },
                    {
                        label: t('field.probe.lastCheck'),
                        value: <DateTime value={summary.last_check_at} relative empty='-' />,
                    },
                    {
                        label: t('field.probe.nextCheck'),
                        value: <DateTime value={summary.next_check_at} relative empty='-' />,
                    },
                ]}
            />
        </div>
    );
}
