import type { ParseKeys } from 'i18next';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { cancelJob, createJob, listJobs, ApiError } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { JobList } from '../components/domain/JobList';
import { MailboxSelect } from '../components/domain/MailboxSelect';
import { Alert } from '../components/ui/Alert';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { ConfirmDialog } from '../components/ui/ConfirmDialog';
import { ErrorState } from '../components/ui/ErrorState';
import { SelectField } from '../components/ui/Field';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useToast } from '../components/ui/Toast';
import { JOB_LIST_LIMIT, JOB_POLL_INTERVAL_MS } from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { useMailboxes } from '../hooks/useMailboxes';
import { usePolling } from '../hooks/usePolling';
import { JOB_KINDS, type JobDTO, type ToolKind } from '../types';

type AnalyzeScope = 'needs' | 'all';
/** How far back sync / fetch search: the mailbox's fetch window, or the whole folder (job target '*'). */
type FetchRange = 'default' | 'all';
type FetchKind = 'sync' | 'fetch';

const isFetchKind = (kind: ToolKind): kind is FetchKind => kind === 'sync' || kind === 'fetch';

// Every tool is run by administrators only; members see the cards and the job history.
const TOOL_META: Record<ToolKind, { icon: string; danger: boolean }> = {
    sync: { icon: 'sync', danger: false },
    fetch: { icon: 'move_to_inbox', danger: false },
    group: { icon: 'category', danger: false },
    analyze: { icon: 'psychology', danger: false },
    reindex: { icon: 'refresh', danger: true },
    reclassify: { icon: 'rule', danger: false },
    cleanup: { icon: 'cleaning_services', danger: false },
};

function emptyTargets(): Record<ToolKind, string> {
    return { sync: '', fetch: '', group: '', analyze: '', reindex: '', reclassify: '', cleanup: '' };
}

export function ToolsPage() {
    const { t, i18n } = useTranslation();
    // Only the tools that need a warning have one in the language files.
    const toolWarning = (kind: ToolKind): string | null => {
        const key = `page.tools.tool.${kind}.warning` as ParseKeys;
        return i18n.exists(key) ? String(t(key)) : null;
    };
    useDocumentTitle(t('layout.nav.tools'));
    const { isAdmin } = useAuth();
    const toast = useToast();
    const { mailboxes } = useMailboxes();
    const jobs = useAsync(() => listJobs(JOB_LIST_LIMIT), []);
    // Selected mailbox per tool ("" = all addresses).
    const [targets, setTargets] = useState<Record<ToolKind, string>>(emptyTargets);
    const [scope, setScope] = useState<AnalyzeScope>('needs');
    const [fetchRanges, setFetchRanges] = useState<Record<FetchKind, FetchRange>>({
        sync: 'default',
        fetch: 'default',
    });
    const [pending, setPending] = useState<ToolKind | null>(null);
    const [submitting, setSubmitting] = useState(false);
    const [canceling, setCanceling] = useState<number | null>(null);

    const hasActive = (jobs.data ?? []).some(j => j.status === 'queued' || j.status === 'running');
    usePolling(jobs.reload, !jobs.loading, hasActive ? JOB_POLL_INTERVAL_MS : JOB_POLL_INTERVAL_MS * 5);

    const toolTitle = (kind: ToolKind) => t(`page.tools.tool.${kind}.title`);
    const canRun = () => isAdmin && mailboxes.length > 0;
    const targetLabel = (kind: ToolKind) => {
        const v = targets[kind];
        return v ? (mailboxes.find(mb => String(mb.id) === v)?.address ?? v) : t('component.mailboxSelect.all');
    };

    async function run(kind: ToolKind) {
        setSubmitting(true);
        try {
            const mailboxId = targets[kind] ? Number(targets[kind]) : null;
            const target =
                kind === 'analyze'
                    ? scope === 'all'
                        ? '*'
                        : ''
                    : isFetchKind(kind)
                      ? fetchRanges[kind] === 'all'
                          ? '*'
                          : ''
                      : undefined;
            // One job per click: with mailbox_id null the server expands it to one child job per address.
            const r = await createJob({ kind, mailbox_id: mailboxId, target });
            if (r.created) toast.success(t('result.job.queued', { tool: toolTitle(kind) }));
            else toast.info(t('result.job.alreadyActive'));
            setPending(null);
            await jobs.reload();
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setSubmitting(false);
        }
    }

    async function cancel(job: JobDTO) {
        setCanceling(job.id);
        try {
            await cancelJob(job.id);
            toast.success(t('result.job.canceled'));
            await jobs.reload();
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setCanceling(null);
        }
    }

    return (
        <PageContainer wide>
            <PageHeader title={t('layout.nav.tools')} description={t('page.tools.description')} />

            {!isAdmin && (
                <Alert tone='info' className='mb-4' title={t('page.tools.adminOnlyTitle')}>
                    {t('page.tools.adminOnly')}
                </Alert>
            )}

            <ul className='grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3'>
                {JOB_KINDS.map(kind => {
                    const meta = TOOL_META[kind];
                    const warning = toolWarning(kind);
                    return (
                        <li key={kind}>
                            <Card className='flex h-full flex-col gap-3'>
                                <div className='flex items-start gap-2'>
                                    <Icon name={meta.icon} className='mt-0.5 text-[28px] text-accent' />
                                    <div className='min-w-0 flex-1'>
                                        <h2 className='text-lg font-semibold text-ink'>{toolTitle(kind)}</h2>
                                    </div>
                                </div>
                                <p className='text-sm text-muted'>{t(`page.tools.tool.${kind}.description`)}</p>
                                {warning && (
                                    <p className='inline-flex items-start gap-1 text-sm text-warning'>
                                        <Icon name='warning' className='mt-0.5 shrink-0 text-[18px]' />
                                        {warning}
                                    </p>
                                )}
                                {isAdmin ? (
                                    <div className='mt-auto flex flex-col gap-3'>
                                        <MailboxSelect
                                            mailboxes={mailboxes}
                                            value={targets[kind]}
                                            onChange={v => setTargets(prev => ({ ...prev, [kind]: v }))}
                                            allowAll
                                            label={t('field.job.target')}
                                        />
                                        {kind === 'analyze' && (
                                            <SelectField
                                                label={t('page.tools.analyzeScope')}
                                                value={scope}
                                                onChange={e => setScope(e.target.value as AnalyzeScope)}
                                            >
                                                <option value='needs'>{t('page.tools.scopeNeeds')}</option>
                                                <option value='all'>{t('component.jobList.targetAll')}</option>
                                            </SelectField>
                                        )}
                                        {isFetchKind(kind) && (
                                            <SelectField
                                                label={t('page.tools.fetchRange')}
                                                value={fetchRanges[kind]}
                                                onChange={e =>
                                                    setFetchRanges(prev => ({
                                                        ...prev,
                                                        [kind]: e.target.value as FetchRange,
                                                    }))
                                                }
                                            >
                                                <option value='default'>{t('page.tools.fetchRangeDefault')}</option>
                                                <option value='all'>{t('component.jobList.targetAllTime')}</option>
                                            </SelectField>
                                        )}
                                        <Button
                                            variant='primary'
                                            icon='play_arrow'
                                            disabled={!canRun()}
                                            onClick={() => setPending(kind)}
                                            aria-label={t('page.tools.runFor', { tool: toolTitle(kind) })}
                                        >
                                            {t('page.tools.run')}
                                        </Button>
                                    </div>
                                ) : (
                                    <p className='mt-auto inline-flex items-center gap-1 text-sm text-muted'>
                                        <Icon name='lock' className='text-[18px]' />
                                        {t('page.tools.readOnly')}
                                    </p>
                                )}
                            </Card>
                        </li>
                    );
                })}
            </ul>

            <section className='mt-8'>
                <CardHeader
                    title={t('page.tools.jobs')}
                    description={t('page.tools.jobsHint')}
                    actions={
                        <Button size='sm' icon='refresh' loading={jobs.refreshing} onClick={() => void jobs.reload()}>
                            {t('common.refresh')}
                        </Button>
                    }
                />
                {jobs.loading ? (
                    <LoadingBlock />
                ) : jobs.error || !jobs.data ? (
                    <ErrorState
                        message={t(jobs.error?.key ?? 'system.internal', jobs.error?.params)}
                        onRetry={() => void jobs.reload()}
                    />
                ) : (
                    <JobList jobs={jobs.data} onCancel={isAdmin ? cancel : undefined} cancelingId={canceling} />
                )}
            </section>

            <ConfirmDialog
                open={pending !== null}
                title={pending ? t('page.tools.confirmTitle', { tool: toolTitle(pending) }) : ''}
                message={
                    pending && (
                        <>
                            <p>
                                {t('page.tools.confirmMessage', {
                                    tool: toolTitle(pending),
                                    target: targetLabel(pending),
                                })}
                            </p>
                            {isFetchKind(pending) && fetchRanges[pending] === 'all' && (
                                <p className='mt-2'>{t('page.tools.confirmAllTime')}</p>
                            )}
                            {!targets[pending] && <p className='mt-2'>{t('page.tools.allNote')}</p>}
                            {toolWarning(pending) && <p className='mt-2 text-warning'>{toolWarning(pending)}</p>}
                        </>
                    )
                }
                confirmLabel={t('page.tools.run')}
                danger={pending !== null && TOOL_META[pending].danger}
                busy={submitting}
                onConfirm={() => pending && void run(pending)}
                onCancel={() => setPending(null)}
            />
        </PageContainer>
    );
}
