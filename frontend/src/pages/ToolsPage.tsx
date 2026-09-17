import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { cancelJob, createJob, listJobs } from '../api/client';
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
import { errorMessage } from '../utils/errors';

type AnalyzeScope = 'needs' | 'all';

// Every tool is run by administrators only; members see the cards and the job history.
const TOOL_META: Record<ToolKind, { icon: string; danger: boolean }> = {
    sync: { icon: 'sync', danger: false },
    fetch: { icon: 'move_to_inbox', danger: false },
    group: { icon: 'category', danger: false },
    analyze: { icon: 'psychology', danger: false },
    reindex: { icon: 'refresh', danger: true },
    reclassify: { icon: 'rule', danger: false },
};

function emptyTargets(): Record<ToolKind, string> {
    return { sync: '', fetch: '', group: '', analyze: '', reindex: '', reclassify: '' };
}

export function ToolsPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.tools'));
    const { isAdmin } = useAuth();
    const toast = useToast();
    const { mailboxes } = useMailboxes();
    const jobs = useAsync(() => listJobs(JOB_LIST_LIMIT), []);
    // Selected mailbox per tool ("" = all addresses).
    const [targets, setTargets] = useState<Record<ToolKind, string>>(emptyTargets);
    const [scope, setScope] = useState<AnalyzeScope>('needs');
    const [pending, setPending] = useState<ToolKind | null>(null);
    const [submitting, setSubmitting] = useState(false);
    const [canceling, setCanceling] = useState<number | null>(null);

    const hasActive = (jobs.data ?? []).some(j => j.status === 'queued' || j.status === 'running');
    usePolling(jobs.reload, !jobs.loading, hasActive ? JOB_POLL_INTERVAL_MS : JOB_POLL_INTERVAL_MS * 5);

    const toolTitle = (kind: ToolKind) => t(`tools.${kind}.title`);
    const canRun = () => isAdmin && mailboxes.length > 0;
    const targetLabel = (kind: ToolKind) => {
        const v = targets[kind];
        return v ? (mailboxes.find(mb => String(mb.id) === v)?.address ?? v) : t('mailbox.all');
    };

    async function run(kind: ToolKind) {
        setSubmitting(true);
        try {
            const mailboxId = targets[kind] ? Number(targets[kind]) : null;
            const target = kind === 'analyze' ? (scope === 'all' ? '*' : '') : undefined;
            // One job per click: with mailbox_id null the server expands it to one child job per address.
            const r = await createJob({ kind, mailbox_id: mailboxId, target });
            if (r.created) toast.success(t('tools.queued', { tool: toolTitle(kind) }));
            else toast.info(t('jobs.alreadyActive'));
            setPending(null);
            await jobs.reload();
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setSubmitting(false);
        }
    }

    async function cancel(job: JobDTO) {
        setCanceling(job.id);
        try {
            await cancelJob(job.id);
            toast.success(t('jobs.canceled'));
            await jobs.reload();
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setCanceling(null);
        }
    }

    return (
        <PageContainer wide>
            <PageHeader title={t('nav.tools')} description={t('tools.description')} />

            {!isAdmin && (
                <Alert tone='info' className='mb-4' title={t('tools.adminOnlyTitle')}>
                    {t('tools.adminOnly')}
                </Alert>
            )}

            <ul className='grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3'>
                {JOB_KINDS.map(kind => {
                    const meta = TOOL_META[kind];
                    const warning = t(`tools.${kind}.warning`);
                    return (
                        <li key={kind}>
                            <Card className='flex h-full flex-col gap-3'>
                                <div className='flex items-start gap-2'>
                                    <Icon name={meta.icon} className='mt-0.5 text-[28px] text-accent' />
                                    <div className='min-w-0 flex-1'>
                                        <h2 className='text-lg font-semibold text-ink'>{toolTitle(kind)}</h2>
                                    </div>
                                </div>
                                <p className='text-sm text-muted'>{t(`tools.${kind}.description`)}</p>
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
                                            label={t('tools.target')}
                                        />
                                        {kind === 'analyze' && (
                                            <SelectField
                                                label={t('tools.analyzeScope')}
                                                value={scope}
                                                onChange={e => setScope(e.target.value as AnalyzeScope)}
                                            >
                                                <option value='needs'>{t('tools.scopeNeeds')}</option>
                                                <option value='all'>{t('tools.scopeAll')}</option>
                                            </SelectField>
                                        )}
                                        <Button
                                            variant='primary'
                                            icon='play_arrow'
                                            disabled={!canRun()}
                                            onClick={() => setPending(kind)}
                                            aria-label={t('tools.runAria', { tool: toolTitle(kind) })}
                                        >
                                            {t('tools.run')}
                                        </Button>
                                    </div>
                                ) : (
                                    <p className='mt-auto inline-flex items-center gap-1 text-sm text-muted'>
                                        <Icon name='lock' className='text-[18px]' />
                                        {t('tools.readOnly')}
                                    </p>
                                )}
                            </Card>
                        </li>
                    );
                })}
            </ul>

            <section className='mt-8'>
                <CardHeader
                    title={t('tools.jobs')}
                    description={t('tools.jobsHint')}
                    actions={
                        <Button size='sm' icon='refresh' loading={jobs.refreshing} onClick={() => void jobs.reload()}>
                            {t('common.refresh')}
                        </Button>
                    }
                />
                {jobs.loading ? (
                    <LoadingBlock />
                ) : jobs.error || !jobs.data ? (
                    <ErrorState message={errorMessage(jobs.error, t)} onRetry={() => void jobs.reload()} />
                ) : (
                    <JobList jobs={jobs.data} onCancel={isAdmin ? cancel : undefined} cancelingId={canceling} />
                )}
            </section>

            <ConfirmDialog
                open={pending !== null}
                title={pending ? t('tools.confirmTitle', { tool: toolTitle(pending) }) : ''}
                message={
                    pending && (
                        <>
                            <p>
                                {t('tools.confirmMessage', { tool: toolTitle(pending), target: targetLabel(pending) })}
                            </p>
                            {!targets[pending] && <p className='mt-2'>{t('tools.allNote')}</p>}
                            {t(`tools.${pending}.warning`) && (
                                <p className='mt-2 text-warning'>{t(`tools.${pending}.warning`)}</p>
                            )}
                        </>
                    )
                }
                confirmLabel={t('tools.run')}
                danger={pending !== null && TOOL_META[pending].danger}
                busy={submitting}
                onConfirm={() => pending && void run(pending)}
                onCancel={() => setPending(null)}
            />
        </PageContainer>
    );
}
