import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router-dom';
import { analyzeGroup, getGroup, getJob, getMailbox, setGroupState } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import {
    ActionableBadge,
    BounceKindBadge,
    CategoryBadge,
    GroupStateBadge,
    ReportStatusBadge,
    ResponsibleBadge,
    SeverityBadge,
} from '../components/domain/StatusBadges';
import { Alert } from '../components/ui/Alert';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { CopyButton } from '../components/ui/CopyButton';
import { DateTime } from '../components/ui/DateTime';
import { DescriptionList } from '../components/ui/DescriptionList';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Icon } from '../components/ui/Icon';
import { Markdown } from '../components/ui/Markdown';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { Table, type Column } from '../components/ui/Table';
import { useToast } from '../components/ui/Toast';
import { JOB_POLL_INTERVAL_MS } from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { usePolling } from '../hooks/usePolling';
import type { GroupState, JobDTO, MessageDTO, ReportDTO } from '../types';
import { categoryDescription, groupHeadline } from '../utils/category';
import { errorMessage } from '../utils/errors';
import { formatDateTime } from '../utils/format';

const STATES: GroupState[] = ['open', 'resolved', 'ignored'];

function StatChips({ title, icon, values }: { title: string; icon: string; values: string[] }) {
    const { t } = useTranslation();
    const [expanded, setExpanded] = useState(false);
    const LIMIT = 12;
    const shown = expanded ? values : values.slice(0, LIMIT);
    return (
        <div>
            <div className='flex flex-wrap items-center justify-between gap-2'>
                <h3 className='inline-flex items-center gap-1 text-sm font-medium text-muted'>
                    <Icon name={icon} className='text-[18px]' />
                    {title} ({values.length})
                </h3>
                {values.length > 0 && (
                    <CopyButton text={values.join('\n')} label={t('group.copyAll', { what: title })} />
                )}
            </div>
            {values.length === 0 ? (
                <p className='mt-1 text-sm text-muted'>-</p>
            ) : (
                <ul className='mt-1 flex flex-wrap gap-1.5'>
                    {shown.map(v => (
                        <li
                            key={v}
                            className='inline-flex max-w-full items-center rounded-md bg-well pl-2 font-mono text-sm text-ink'
                        >
                            <span className='truncate py-1'>{v}</span>
                            <CopyButton text={v} label={t('group.copyValue', { value: v })} />
                        </li>
                    ))}
                    {values.length > LIMIT && (
                        <li>
                            <Button size='sm' variant='ghost' onClick={() => setExpanded(v => !v)}>
                                {expanded
                                    ? t('common.showLess')
                                    : t('common.showMore', { count: values.length - LIMIT })}
                            </Button>
                        </li>
                    )}
                </ul>
            )}
        </div>
    );
}

/** The newest failed report when it is more recent than the completed report on display. */
function newestFailedAfter(reports: ReportDTO[], shown: ReportDTO | null): ReportDTO | null {
    const time = (r: ReportDTO) => Date.parse(r.finished_at ?? r.created_at) || 0;
    const failed = reports.filter(r => r.status === 'error' && (!shown || r.id !== shown.id));
    if (failed.length === 0) return null;
    const newest = failed.reduce((a, b) => (time(b) > time(a) ? b : a));
    if (shown && time(newest) <= time(shown)) return null;
    return newest;
}

function ReportView({ report }: { report: ReportDTO }) {
    const { t } = useTranslation();
    return (
        <div>
            <div className='flex flex-wrap items-center gap-2 text-sm text-muted'>
                <ReportStatusBadge status={report.status} />
                {report.severity && <SeverityBadge severity={report.severity} />}
                {report.responsible && <ResponsibleBadge responsible={report.responsible} />}
                <span>
                    {t('report.provider')}: {report.provider || '-'}
                </span>
                <span>{t('report.sampled', { count: report.message_count })}</span>
                <span>
                    <DateTime value={report.finished_at ?? report.created_at} />
                </span>
            </div>
            {report.status === 'error' && report.error_message && (
                <Alert tone='danger' className='mt-3'>
                    {report.error_message}
                </Alert>
            )}
            {report.summary && <p className='mt-3 rounded-md bg-well p-3 text-base text-ink'>{report.summary}</p>}
            {report.report_markdown ? (
                <div className='mt-4'>
                    <Markdown source={report.report_markdown} />
                </div>
            ) : (
                report.status !== 'running' && <p className='mt-3 text-sm text-muted'>{t('report.noBody')}</p>
            )}
        </div>
    );
}

export function AlertGroupDetailPage() {
    const { t } = useTranslation();
    const { mailboxId = '', groupKey = '' } = useParams();
    const id = Number(mailboxId);
    const navigate = useNavigate();
    const toast = useToast();
    const { isAdmin } = useAuth();
    const mailbox = useAsync(() => getMailbox(id), [id]);
    const detail = useAsync(() => getGroup(id, groupKey), [id, groupKey]);
    const [job, setJob] = useState<JobDTO | null>(null);
    const [analyzing, setAnalyzing] = useState(false);
    const [changing, setChanging] = useState<GroupState | null>(null);
    const group = detail.data?.group;
    const headline = group ? groupHeadline(group, t) : '';
    useDocumentTitle(headline || t('nav.alerts'));
    const reportRunning =
        group?.report_status === 'running' || (job !== null && (job.status === 'queued' || job.status === 'running'));

    // While an analysis job is active, poll the job and refresh the group when it ends.
    usePolling(
        async () => {
            if (job) {
                const j = await getJob(job.id);
                setJob(j);
                if (j.status === 'done' || j.status === 'error' || j.status === 'canceled') {
                    await detail.reload();
                    if (j.status === 'error') toast.error(j.error_message || t('report.failed'));
                    else if (j.status === 'done') toast.success(t('report.completed'));
                    setJob(null);
                }
            } else {
                await detail.reload();
            }
        },
        reportRunning,
        JOB_POLL_INTERVAL_MS
    );

    useEffect(() => {
        setJob(null);
    }, [id, groupKey]);

    async function analyze() {
        setAnalyzing(true);
        try {
            const r = await analyzeGroup(id, groupKey);
            setJob(r.job);
            toast.info(r.created ? t('report.queued') : t('jobs.alreadyActive'));
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setAnalyzing(false);
        }
    }

    async function changeState(state: GroupState) {
        setChanging(state);
        try {
            const updated = await setGroupState(id, groupKey, state);
            detail.setData(prev =>
                prev
                    ? {
                          ...prev,
                          group: updated ?? { ...prev.group, state, state_updated_at: new Date().toISOString() },
                      }
                    : prev
            );
            toast.success(t('group.stateChanged', { state: t(`groupState.${state}`) }));
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setChanging(null);
        }
    }

    if (detail.loading) return <LoadingBlock />;
    if (detail.error || !detail.data || !group) {
        return (
            <PageContainer>
                <ErrorState message={errorMessage(detail.error, t)} onRetry={() => void detail.reload()} />
            </PageContainer>
        );
    }

    const { stats, report, reports, messages } = detail.data;
    const history = reports.filter(r => !report || r.id !== report.id);
    // A re-analysis that failed after the shown report was completed: keep the report, but say so.
    const latestFailed = newestFailedAfter(reports, report);
    const description = categoryDescription(group.category, t);

    const messageColumns: Column<MessageDTO>[] = [
        {
            key: 'date',
            header: t('mail.date'),
            cell: m => <DateTime value={m.date ?? m.received_at} />,
            className: 'whitespace-nowrap',
        },
        {
            key: 'subject',
            header: t('mail.subject'),
            cell: m => <span className='break-words'>{m.subject || t('mail.noSubject')}</span>,
            primary: true,
        },
        { key: 'from', header: t('mail.from'), cell: m => <span className='break-all'>{m.from_address}</span> },
        {
            key: 'kind',
            header: t('mail.kind'),
            cell: m => <BounceKindBadge kind={m.bounce_kind} isBounce={m.is_bounce} />,
        },
    ];

    return (
        <PageContainer wide>
            <PageHeader
                title={headline}
                crumbs={[
                    { label: t('nav.alerts'), to: '/alerts' },
                    { label: mailbox.data?.address ?? '...', to: `/alerts/${id}` },
                    { label: t('group.detail') },
                ]}
                actions={
                    isAdmin && (
                        <div className='flex flex-wrap gap-2' role='group' aria-label={t('group.changeState')}>
                            {STATES.filter(s => s !== group.state).map(s => (
                                <Button
                                    key={s}
                                    size='sm'
                                    variant={s === 'resolved' ? 'primary' : 'secondary'}
                                    icon={s === 'open' ? 'undo' : s === 'resolved' ? 'check_circle' : 'visibility_off'}
                                    loading={changing === s}
                                    disabled={changing !== null}
                                    onClick={() => void changeState(s)}
                                >
                                    {t(`group.markAs.${s}`)}
                                </Button>
                            ))}
                        </div>
                    )
                }
            />

            <div className='grid grid-cols-1 gap-6 lg:grid-cols-3'>
                <div className='flex flex-col gap-6 lg:col-span-2'>
                    <Card>
                        <CardHeader title={t('group.overview')} />
                        <div className='mb-4 flex flex-wrap gap-2'>
                            <CategoryBadge category={group.category} />
                            <ActionableBadge actionable={group.actionable} />
                            <GroupStateBadge state={group.state} />
                            {group.actionable && <SeverityBadge severity={group.report_severity} />}
                            <ResponsibleBadge responsible={group.responsible} />
                            {group.actionable && group.needs_analysis && group.report_status !== 'running' && (
                                <Badge tone='warning' icon='pending_actions'>
                                    {t('group.needsAnalysis')}
                                </Badge>
                            )}
                        </div>
                        {description && (
                            <p className='mb-4 flex items-start gap-2 rounded-md bg-well p-3 text-base text-ink'>
                                <Icon name='lightbulb' className='mt-0.5 shrink-0 text-[20px] text-accent' />
                                <span>{description}</span>
                            </p>
                        )}
                        <DescriptionList
                            items={[
                                {
                                    label: t('group.unitValue'),
                                    value: group.unit_value ? (
                                        <span className='inline-flex max-w-full items-center gap-1'>
                                            <code className='break-all font-mono text-base'>{group.unit_value}</code>
                                            <CopyButton
                                                text={group.unit_value}
                                                label={t('group.copyUnit', { value: group.unit_value })}
                                            />
                                        </span>
                                    ) : (
                                        '-'
                                    ),
                                },
                                {
                                    label: t('group.authority'),
                                    value: group.authority ? (
                                        <code className='break-all font-mono text-base'>{group.authority}</code>
                                    ) : (
                                        '-'
                                    ),
                                },
                                { label: t('group.recipientDomain'), value: group.recipient_domain || '-' },
                                {
                                    label: t('group.statusCode'),
                                    value: group.status_code || '-',
                                },
                                { label: t('group.messageCount'), value: group.message_count },
                                { label: t('group.recipientCount'), value: group.recipient_count },
                                { label: t('group.ipCount'), value: group.remote_ip_count },
                                { label: t('group.firstSeen'), value: <DateTime value={group.first_seen} /> },
                                { label: t('group.lastSeen'), value: <DateTime value={group.last_seen} /> },
                                { label: t('group.stateUpdated'), value: <DateTime value={group.state_updated_at} /> },
                                {
                                    label: t('group.diagnosticTemplate'),
                                    value: (
                                        <code className='block break-words rounded-md bg-well p-2 font-mono text-sm'>
                                            {group.diagnostic_template || '-'}
                                        </code>
                                    ),
                                    wide: true,
                                },
                                {
                                    label: t('group.key'),
                                    value: <code className='font-mono text-sm'>{group.group_key}</code>,
                                    wide: true,
                                },
                            ]}
                        />
                    </Card>

                    <Card>
                        <CardHeader
                            title={t('report.title')}
                            actions={
                                group.actionable && isAdmin ? (
                                    <Button
                                        size='sm'
                                        variant='primary'
                                        icon='psychology'
                                        loading={analyzing}
                                        disabled={reportRunning}
                                        onClick={() => void analyze()}
                                    >
                                        {reportRunning ? t('report.running') : t('report.reanalyze')}
                                    </Button>
                                ) : undefined
                            }
                        />
                        {latestFailed && !reportRunning && (
                            <Alert tone='warning' className='mb-4'>
                                {t('report.latestFailed', {
                                    message: latestFailed.error_message || t('report.failed'),
                                    time: formatDateTime(latestFailed.finished_at ?? latestFailed.created_at),
                                })}
                            </Alert>
                        )}
                        {!group.actionable && (
                            <Alert tone='info' className='mb-4' title={t('report.excludedTitle')}>
                                {t('report.excluded')}
                            </Alert>
                        )}
                        {reportRunning && (
                            <Alert tone='info' className='mb-4'>
                                <span className='inline-flex items-center gap-2'>
                                    <Icon name='autorenew' className='animate-spin text-[18px]' />
                                    {job?.progress || t('report.runningHint')}
                                </span>
                            </Alert>
                        )}
                        {report ? (
                            <ReportView report={report} />
                        ) : group.actionable ? (
                            <EmptyState
                                icon='psychology'
                                title={t('report.none')}
                                description={isAdmin ? t('report.noneHint') : t('report.noneHintMember')}
                            />
                        ) : null}
                        {history.length > 0 && (
                            <details className='mt-6'>
                                <summary className='cursor-pointer text-base font-medium text-muted hover:text-ink'>
                                    {t('report.history', { count: history.length })}
                                </summary>
                                <ul className='mt-3 flex flex-col gap-3'>
                                    {history.map(r => (
                                        <li key={r.id} className='rounded-lg border border-line p-3'>
                                            <details>
                                                <summary className='flex cursor-pointer flex-wrap items-center gap-2 text-sm'>
                                                    <ReportStatusBadge status={r.status} />
                                                    {r.severity && <SeverityBadge severity={r.severity} />}
                                                    <span className='text-muted'>
                                                        {formatDateTime(r.finished_at ?? r.created_at)}
                                                    </span>
                                                    <span className='text-ink'>{r.summary}</span>
                                                </summary>
                                                <div className='mt-3'>
                                                    <ReportView report={r} />
                                                </div>
                                            </details>
                                        </li>
                                    ))}
                                </ul>
                            </details>
                        )}
                    </Card>

                    <section>
                        <CardHeader title={t('group.messagesTitle', { count: messages.length })} />
                        <Table
                            columns={messageColumns}
                            rows={messages}
                            rowKey={m => m.message_key}
                            caption={t('group.messagesTitle', { count: messages.length })}
                            onRowClick={m => navigate(`/mails/${id}/${encodeURIComponent(m.message_key)}`)}
                            rowAriaLabel={m => t('mail.openAria', { subject: m.subject || t('mail.noSubject') })}
                            emptyState={<EmptyState title={t('group.noMessages')} />}
                            dense
                        />
                    </section>
                </div>

                <aside>
                    <Card className='flex flex-col gap-5'>
                        <CardHeader title={t('group.stats')} />
                        <StatChips title={t('group.recipientsList')} icon='person' values={stats.recipients} />
                        <StatChips title={t('group.ipsList')} icon='router' values={stats.remote_ips} />
                        <StatChips title={t('group.mtasList')} icon='dns' values={stats.remote_mtas} />
                    </Card>
                </aside>
            </div>
        </PageContainer>
    );
}
