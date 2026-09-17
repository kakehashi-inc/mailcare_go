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
import type { JobDTO, JobKind } from '../types';
import { errorMessage } from '../utils/errors';

type ToolKind = Exclude<JobKind, 'check'>;
type AnalyzeScope = 'needs' | 'all';

const TOOLS: { kind: ToolKind; icon: string; danger: boolean }[] = [
    { kind: 'reindex', icon: 'refresh', danger: true },
    { kind: 'reclassify', icon: 'rule', danger: false },
    { kind: 'analyze', icon: 'psychology', danger: false },
];

export function ToolsPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.tools'));
    const { isAdmin } = useAuth();
    const toast = useToast();
    const { mailboxes } = useMailboxes();
    const jobs = useAsync(() => listJobs(JOB_LIST_LIMIT), []);
    const [mailboxId, setMailboxId] = useState('');
    const [scope, setScope] = useState<AnalyzeScope>('needs');
    const [pending, setPending] = useState<ToolKind | null>(null);
    const [submitting, setSubmitting] = useState(false);
    const [canceling, setCanceling] = useState<number | null>(null);

    const hasActive = (jobs.data ?? []).some(j => j.status === 'queued' || j.status === 'running');
    usePolling(jobs.reload, !jobs.loading, hasActive ? JOB_POLL_INTERVAL_MS : JOB_POLL_INTERVAL_MS * 5);

    const enabledMailboxes = mailboxes.filter(mb => mb.enabled);
    const targetLabel = mailboxId
        ? (mailboxes.find(mb => String(mb.id) === mailboxId)?.address ?? mailboxId)
        : t('mailbox.all');

    async function run(kind: ToolKind) {
        setSubmitting(true);
        try {
            const target = kind === 'analyze' ? (scope === 'all' ? '*' : '') : undefined;
            let created = 0;
            let skipped = 0;
            if (kind === 'analyze' && !mailboxId) {
                // Analysis jobs are per mailbox: queue one for each enabled mailbox.
                for (const mb of enabledMailboxes) {
                    const r = await createJob({ kind, mailbox_id: mb.id, target });
                    if (r.created) created++;
                    else skipped++;
                }
            } else {
                const r = await createJob({ kind, mailbox_id: mailboxId ? Number(mailboxId) : undefined, target });
                if (r.created) created++;
                else skipped++;
            }
            if (created > 0) toast.success(t('tools.queued', { tool: t(`jobKind.${kind}`) }));
            if (skipped > 0) toast.info(t('jobs.alreadyActive'));
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

            <Card className='mb-6'>
                <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
                    <MailboxSelect
                        mailboxes={mailboxes}
                        value={mailboxId}
                        onChange={setMailboxId}
                        allowAll
                        label={t('tools.target')}
                        disabled={!isAdmin}
                        hint={t('tools.targetHint')}
                    />
                    <SelectField
                        label={t('tools.analyzeScope')}
                        value={scope}
                        onChange={e => setScope(e.target.value as AnalyzeScope)}
                        disabled={!isAdmin}
                        hint={t('tools.analyzeScopeHint')}
                    >
                        <option value='needs'>{t('tools.scopeNeeds')}</option>
                        <option value='all'>{t('tools.scopeAll')}</option>
                    </SelectField>
                </div>
            </Card>

            <ul className='grid grid-cols-1 gap-4 md:grid-cols-3'>
                {TOOLS.map(tool => (
                    <li key={tool.kind}>
                        <Card className='flex h-full flex-col gap-3'>
                            <div className='flex items-center gap-2'>
                                <Icon name={tool.icon} className='text-[28px] text-accent' />
                                <h2 className='text-lg font-semibold text-ink'>{t(`jobKind.${tool.kind}`)}</h2>
                            </div>
                            <p className='flex-1 text-sm text-muted'>{t(`tools.${tool.kind}.description`)}</p>
                            {tool.danger && (
                                <p className='inline-flex items-start gap-1 text-sm text-warning'>
                                    <Icon name='warning' className='mt-0.5 text-[18px]' />
                                    {t(`tools.${tool.kind}.warning`)}
                                </p>
                            )}
                            <Button
                                variant='primary'
                                icon='play_arrow'
                                disabled={
                                    !isAdmin || (tool.kind === 'analyze' && !mailboxId && enabledMailboxes.length === 0)
                                }
                                onClick={() => setPending(tool.kind)}
                                aria-label={t('tools.runAria', { tool: t(`jobKind.${tool.kind}`) })}
                                aria-describedby={!isAdmin ? 'tools-admin-only' : undefined}
                            >
                                {t('tools.run')}
                            </Button>
                        </Card>
                    </li>
                ))}
            </ul>
            {!isAdmin && (
                <p id='tools-admin-only' className='sr-only'>
                    {t('tools.adminOnly')}
                </p>
            )}

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
                title={pending ? t('tools.confirmTitle', { tool: t(`jobKind.${pending}`) }) : ''}
                message={
                    pending && (
                        <>
                            <p>{t('tools.confirmMessage', { tool: t(`jobKind.${pending}`), target: targetLabel })}</p>
                            {pending === 'reindex' && <p className='mt-2 text-warning'>{t('tools.reindex.warning')}</p>}
                            {pending === 'analyze' && !mailboxId && (
                                <p className='mt-2'>{t('tools.analyzeAllNote', { count: enabledMailboxes.length })}</p>
                            )}
                        </>
                    )
                }
                confirmLabel={t('tools.run')}
                danger={pending === 'reindex'}
                busy={submitting}
                onConfirm={() => pending && void run(pending)}
                onCancel={() => setPending(null)}
            />
        </PageContainer>
    );
}
