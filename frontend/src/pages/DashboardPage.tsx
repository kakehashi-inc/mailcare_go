import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { getDashboard } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { JobList } from '../components/domain/JobList';
import { ProbeBadges } from '../components/domain/ProbeBadges';
import { CheckStatusBadge, fetchStatus } from '../components/domain/StatusBadges';
import { Alert } from '../components/ui/Alert';
import { Badge } from '../components/ui/Badge';
import { LinkButton } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { DateTime } from '../components/ui/DateTime';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { DASHBOARD_REFRESH_MS, JOB_POLL_INTERVAL_MS } from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { usePolling } from '../hooks/usePolling';
import type { MailboxDTO, ProbeMailDTO } from '../types';
import { formatMinutes, formatNumber } from '../utils/format';

export function DashboardPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.dashboard'));
    const { isAdmin } = useAuth();
    const { data, error, loading, reload } = useAsync(getDashboard, []);

    // A mailbox is busy while a job that touches its mails (for it or for all mailboxes) is queued or
    // running; the server tells every user which ones (the jobs themselves are for administrators only).
    const busy = new Set(data?.busy_mailbox_ids ?? []);
    const hasActive = busy.size > 0 || (data?.active_jobs.length ?? 0) > 0;
    const syncActive = (mb: MailboxDTO) => busy.has(mb.id);
    // Resolved and ignored groups sent back for a re-check (both "(re)" tabs of Alerts).
    const recheckCount = (mb: MailboxDTO) =>
        mb.stats ? mb.stats.groups.resolved_recheck + mb.stats.groups.ignored_recheck : 0;
    usePolling(reload, !loading, hasActive ? JOB_POLL_INTERVAL_MS : DASHBOARD_REFRESH_MS);

    if (loading) return <LoadingBlock />;
    if (error || !data) {
        return (
            <PageContainer>
                <ErrorState message={t(error?.key ?? 'system.internal', error?.params)} onRetry={() => void reload()} />
            </PageContainer>
        );
    }

    const totals = [
        {
            label: t('page.dashboard.mailboxes'),
            value: data.totals.mailboxes,
            icon: 'alternate_email',
            tone: 'text-ink',
        },
        {
            label: t('field.mailbox.openGroups'),
            value: data.totals.open_groups,
            icon: 'notifications_active',
            tone: data.totals.open_groups > 0 ? 'text-danger' : 'text-ink',
        },
        {
            label: t('field.mailbox.recheckGroups'),
            value: data.totals.recheck_groups,
            icon: 'replay',
            tone: data.totals.recheck_groups > 0 ? 'text-warning' : 'text-ink',
        },
        { label: t('field.mailbox.messages'), value: data.totals.messages, icon: 'mail', tone: 'text-ink' },
        {
            label: t('field.mailbox.targetMessages'),
            value: data.totals.target_messages,
            icon: 'report',
            tone: 'text-ink',
        },
        { label: t('field.mailbox.junkMessages'), value: data.totals.junk_messages, icon: 'block', tone: 'text-ink' },
    ];
    const probe = data.probe;
    const probeFailures = probe.counts.failure + probe.counts.send_failed;
    const probeLatest = new Map(probe.mailboxes.map(m => [m.mailbox_id, m.latest]));
    if (data.totals.unclassified > 0) {
        totals.push({
            label: t('field.mailbox.unclassified'),
            value: data.totals.unclassified,
            icon: 'pending',
            tone: 'text-warning',
        });
    }

    return (
        <PageContainer wide>
            <PageHeader title={t('layout.nav.dashboard')} description={t('page.dashboard.description')} />

            {/* Agent notices are for administrators, who can act on them; members only see the badge state. */}
            {isAdmin && !data.agent.enabled && (
                <Alert tone='info' className='mb-4' title={t('page.dashboard.agentDisabledTitle')}>
                    {t('page.dashboard.agentDisabled')}{' '}
                    <Link to='/settings/general' className='font-medium text-accent underline underline-offset-2'>
                        {t('layout.nav.settingsGeneral')}
                    </Link>
                </Alert>
            )}
            {isAdmin && data.agent.enabled && !data.agent.available && (
                <Alert tone='warning' className='mb-4' title={t('page.dashboard.agentUnavailableTitle')}>
                    {t('page.dashboard.agentUnavailable', { provider: data.agent.provider })}
                </Alert>
            )}

            {probeFailures > 0 && (
                <Alert tone='danger' className='mb-4' title={t('page.dashboard.probeFailureTitle')}>
                    <p>{t('page.dashboard.probeFailure', { count: probeFailures })}</p>
                    <LinkButton size='sm' to='/alerts/probes' icon='forward_to_inbox' className='mt-2'>
                        {t('action.probe.open')}
                    </LinkButton>
                </Alert>
            )}
            {probe.counts.delayed > 0 && (
                <Alert tone='warning' className='mb-4' title={t('page.dashboard.probeDelayTitle')}>
                    <p>{t('page.dashboard.probeDelay', { count: probe.counts.delayed })}</p>
                    <LinkButton size='sm' to='/alerts/probes?filter=open' icon='forward_to_inbox' className='mt-2'>
                        {t('action.probe.open')}
                    </LinkButton>
                </Alert>
            )}

            <section
                aria-label={t('page.dashboard.totals')}
                className={`grid grid-cols-2 gap-3 md:grid-cols-3 ${totals.length > 6 ? 'lg:grid-cols-7' : 'lg:grid-cols-6'}`}
            >
                {totals.map(item => (
                    <Card key={item.label} className='flex items-center gap-3'>
                        <Icon name={item.icon} className='text-[28px] text-muted' />
                        <div className='min-w-0'>
                            <p className='break-words text-sm leading-tight text-muted'>{item.label}</p>
                            <p className={`text-2xl font-bold ${item.tone}`}>{formatNumber(item.value)}</p>
                        </div>
                    </Card>
                ))}
            </section>

            <div className='mt-6 grid grid-cols-1 gap-6 lg:grid-cols-3'>
                <div className='flex flex-col gap-6 lg:col-span-2'>
                    <section>
                        <CardHeader title={t('page.dashboard.mailboxes')} />
                        {data.mailboxes.length === 0 ? (
                            <EmptyState
                                title={t('component.mailboxPicker.empty')}
                                action={
                                    isAdmin ? (
                                        <LinkButton to='/settings/mailboxes/new' variant='primary' icon='add'>
                                            {t('action.mailbox.add')}
                                        </LinkButton>
                                    ) : undefined
                                }
                            />
                        ) : (
                            <ul className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                                {data.mailboxes.map(mb => (
                                    <li key={mb.id}>
                                        <Card className='flex h-full flex-col gap-3'>
                                            <div className='flex items-start justify-between gap-2'>
                                                <div className='min-w-0'>
                                                    <Link
                                                        to={`/alerts/${mb.id}`}
                                                        className='flex min-h-tap items-center truncate rounded text-base font-semibold text-ink hover:text-accent hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                                                    >
                                                        {mb.display_name || mb.address}
                                                    </Link>
                                                    {mb.display_name && (
                                                        <p className='truncate text-sm text-muted'>{mb.address}</p>
                                                    )}
                                                </div>
                                                {syncActive(mb) ? (
                                                    <Badge tone='info' icon='autorenew'>
                                                        {t('value.checkStatus.running')}
                                                    </Badge>
                                                ) : (
                                                    <CheckStatusBadge status={fetchStatus(mb)} />
                                                )}
                                            </div>
                                            <dl className='grid grid-cols-3 gap-2 text-sm'>
                                                <div>
                                                    <dt className='text-muted'>{t('field.mailbox.openGroups')}</dt>
                                                    <dd
                                                        className={`text-xl font-bold ${mb.stats?.groups.open ? 'text-danger' : 'text-ink'}`}
                                                    >
                                                        {mb.stats ? formatNumber(mb.stats.groups.open) : '-'}
                                                    </dd>
                                                </div>
                                                <div className='col-span-2'>
                                                    <dt className='text-muted'>{t('field.mailbox.recheckGroups')}</dt>
                                                    <dd
                                                        className={`text-xl font-bold ${recheckCount(mb) ? 'text-warning' : 'text-ink'}`}
                                                    >
                                                        {mb.stats ? formatNumber(recheckCount(mb)) : '-'}
                                                    </dd>
                                                </div>
                                                <div>
                                                    <dt className='text-muted'>{t('field.mailbox.messages')}</dt>
                                                    <dd className='text-xl font-bold text-ink'>
                                                        {mb.stats ? formatNumber(mb.stats.messages) : '-'}
                                                    </dd>
                                                </div>
                                                <div>
                                                    <dt className='text-muted'>{t('field.mailbox.targetMessages')}</dt>
                                                    <dd className='text-xl font-bold text-ink'>
                                                        {mb.stats ? formatNumber(mb.stats.target_messages) : '-'}
                                                    </dd>
                                                </div>
                                                <div>
                                                    <dt className='text-muted'>{t('field.mailbox.junkMessages')}</dt>
                                                    <dd className='text-xl font-bold text-ink'>
                                                        {mb.stats ? formatNumber(mb.stats.junk_messages) : '-'}
                                                    </dd>
                                                </div>
                                                {(mb.stats?.unclassified ?? 0) > 0 && (
                                                    <div>
                                                        <dt className='text-muted'>
                                                            {t('field.mailbox.unclassified')}
                                                        </dt>
                                                        <dd className='text-xl font-bold text-warning'>
                                                            {formatNumber(mb.stats?.unclassified ?? 0)}
                                                        </dd>
                                                    </div>
                                                )}
                                                <div className='col-span-3'>
                                                    <dt className='text-muted'>{t('field.mailbox.lastChecked')}</dt>
                                                    <dd className='text-ink'>
                                                        <DateTime
                                                            value={mb.last_fetched_at}
                                                            relative
                                                            empty={t('value.checkStatus.never')}
                                                        />
                                                    </dd>
                                                </div>
                                            </dl>
                                            <MailboxProbe latest={probeLatest.get(mb.id)} />
                                            {mb.last_fetch_error && (
                                                <p className='break-words text-sm text-danger' role='alert'>
                                                    {isAdmin ? mb.last_fetch_error : t('page.dashboard.fetchError')}
                                                </p>
                                            )}
                                            {!mb.enabled && (
                                                <p className='text-sm text-muted'>
                                                    {t('page.dashboard.mailboxDisabled')}
                                                </p>
                                            )}
                                            <div className='mt-auto flex flex-wrap gap-2'>
                                                <LinkButton size='sm' to={`/alerts/${mb.id}`} icon='notifications'>
                                                    {t('layout.nav.alerts')}
                                                </LinkButton>
                                                <LinkButton size='sm' to={`/mails/${mb.id}`} icon='mail'>
                                                    {t('layout.nav.mails')}
                                                </LinkButton>
                                            </div>
                                        </Card>
                                    </li>
                                ))}
                            </ul>
                        )}
                    </section>
                </div>

                <aside className='flex flex-col gap-6'>
                    <Card>
                        <CardHeader title={t('page.dashboard.schedule')} as='h2' />
                        <dl className='space-y-3 text-base'>
                            <div>
                                <dt className='text-sm text-muted'>{t('page.dashboard.nextCheck')}</dt>
                                <dd className='text-ink'>
                                    {data.next_check_at ? (
                                        <>
                                            <DateTime value={data.next_check_at} />{' '}
                                            <span className='text-sm text-muted'>
                                                (<DateTime value={data.next_check_at} relative />)
                                            </span>
                                        </>
                                    ) : (
                                        t('page.dashboard.noSchedule')
                                    )}
                                </dd>
                            </div>
                            <div>
                                <dt className='text-sm text-muted'>{t('field.setting.checkTimes')}</dt>
                                <dd className='flex flex-wrap gap-2'>
                                    {data.check_times.length === 0 ? (
                                        <span className='text-muted'>{t('page.dashboard.noSchedule')}</span>
                                    ) : (
                                        data.check_times.map(time => (
                                            <span
                                                key={time}
                                                className='rounded-md bg-well px-2 py-0.5 font-mono text-sm text-ink'
                                            >
                                                {time}
                                            </span>
                                        ))
                                    )}
                                </dd>
                            </div>
                            <div>
                                <dt className='text-sm text-muted'>{t('page.dashboard.agent')}</dt>
                                <dd
                                    className='inline-flex items-center gap-1 text-ink'
                                    title={data.agent.enabled ? t('common.enabled') : t('common.disabled')}
                                >
                                    <Icon
                                        name={
                                            !data.agent.enabled
                                                ? 'remove_circle_outline'
                                                : data.agent.available
                                                  ? 'check_circle'
                                                  : 'warning'
                                        }
                                        className={`text-[18px] ${
                                            !data.agent.enabled
                                                ? 'text-muted'
                                                : data.agent.available
                                                  ? 'text-success'
                                                  : 'text-warning'
                                        }`}
                                    />
                                    {data.agent.provider || '-'}
                                </dd>
                            </div>
                        </dl>
                        {isAdmin && (
                            <LinkButton to='/settings/general' size='sm' icon='tune' className='mt-4'>
                                {t('layout.nav.settingsGeneral')}
                            </LinkButton>
                        )}
                    </Card>

                    {(probe.configured || probe.mailboxes.length > 0) && (
                        <Card>
                            <CardHeader title={t('page.dashboard.probe')} as='h2' />
                            <dl className='space-y-3 text-base'>
                                <div>
                                    <dt className='text-sm text-muted'>{t('field.probe.status')}</dt>
                                    <dd className='flex flex-wrap gap-2'>
                                        {probeFailures > 0 && (
                                            <Badge tone='danger' icon='error'>
                                                {t('value.probeStatus.failure')}: {formatNumber(probeFailures)}
                                            </Badge>
                                        )}
                                        {probe.counts.delayed > 0 && (
                                            <Badge tone='warning' icon='hourglass_bottom'>
                                                {t('value.probeStatus.delayed')}: {formatNumber(probe.counts.delayed)}
                                            </Badge>
                                        )}
                                        {probe.last_check_error && (
                                            <Badge tone='warning' icon='sync_problem'>
                                                {t('page.dashboard.probeCheckError')}
                                            </Badge>
                                        )}
                                        {probeFailures + probe.counts.delayed === 0 && !probe.last_check_error && (
                                            <span className='text-ink'>
                                                {probe.enabled
                                                    ? t('page.dashboard.probeOk')
                                                    : t('page.dashboard.probeOff')}
                                            </span>
                                        )}
                                    </dd>
                                    {isAdmin && probe.last_check_error && (
                                        <dd className='mt-1 break-words text-sm text-muted'>
                                            {probe.last_check_error}
                                        </dd>
                                    )}
                                </div>
                                {probe.enabled && (
                                    <>
                                        <div>
                                            <dt className='text-sm text-muted'>{t('field.probe.nextSend')}</dt>
                                            <dd className='text-ink'>
                                                <DateTime value={probe.next_send_at} relative empty='-' />
                                            </dd>
                                        </div>
                                        <div>
                                            <dt className='text-sm text-muted'>{t('field.probe.nextCheck')}</dt>
                                            <dd className='text-ink'>
                                                <DateTime value={probe.next_check_at} relative empty='-' />
                                            </dd>
                                        </div>
                                        <div>
                                            <dt className='text-sm text-muted'>{t('field.probe.thresholds')}</dt>
                                            <dd className='text-ink'>
                                                {t('field.probe.thresholdsValue', {
                                                    delay: formatMinutes(probe.delay_minutes, t),
                                                    failure: formatMinutes(probe.failure_minutes, t),
                                                })}
                                            </dd>
                                        </div>
                                    </>
                                )}
                            </dl>
                            <LinkButton to='/alerts/probes' size='sm' icon='forward_to_inbox' className='mt-4'>
                                {t('action.probe.open')}
                            </LinkButton>
                        </Card>
                    )}

                    {isAdmin && (
                        <Card>
                            <CardHeader
                                title={t('page.dashboard.activeJobs')}
                                actions={
                                    <LinkButton to='/jobs' size='sm' icon='list'>
                                        {t('page.dashboard.allJobs')}
                                    </LinkButton>
                                }
                            />
                            <JobList jobs={data.active_jobs} emptyTitle={t('page.dashboard.noActiveJobs')} />
                            {data.recent_jobs.length > 0 && (
                                <details className='mt-4'>
                                    <summary className='cursor-pointer text-sm font-medium text-muted hover:text-ink'>
                                        {t('page.dashboard.recentJobs')}
                                    </summary>
                                    <div className='mt-2'>
                                        <JobList jobs={data.recent_jobs} />
                                    </div>
                                </details>
                            )}
                        </Card>
                    )}
                </aside>
            </div>
        </PageContainer>
    );
}

/** The newest probe mail of a mailbox, on its card (nothing when it never sent one). */
function MailboxProbe({ latest }: { latest: ProbeMailDTO | null | undefined }) {
    const { t } = useTranslation();
    if (!latest) return null;
    return (
        <div className='flex flex-wrap items-center gap-2 text-sm'>
            <span className='text-muted'>{t('page.dashboard.probe')}</span>
            <ProbeBadges probe={latest} />
        </div>
    );
}
