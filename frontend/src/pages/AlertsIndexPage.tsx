import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { listMailboxes, listProbes } from '../api/client';
import { MailboxPicker } from '../components/domain/MailboxPicker';
import { Badge } from '../components/ui/Badge';
import { Icon } from '../components/ui/Icon';
import { ErrorState } from '../components/ui/ErrorState';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';

export function AlertsIndexPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.alerts'));
    const { data, error, loading, reload } = useAsync(() => listMailboxes(true), []);
    // Only the counts are needed; the list itself is on the probe page.
    const probes = useAsync(() => listProbes({ per_page: 1 }), []);
    const summary = probes.data?.summary;

    return (
        <PageContainer wide>
            <PageHeader title={t('layout.nav.alerts')} description={t('page.alerts.chooseMailbox')} />
            {summary && (summary.configured || summary.mailboxes.length > 0) && (
                <Link
                    to='/alerts/probes'
                    className='mb-6 flex items-center gap-4 rounded-xl border border-line bg-surface p-4 shadow-sm transition-colors hover:border-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                >
                    <Icon name='forward_to_inbox' className='text-[32px] text-accent' />
                    <div className='min-w-0 flex-1'>
                        <span className='block text-lg font-semibold text-ink'>{t('layout.nav.probes')}</span>
                        <p className='mt-1 text-sm text-muted'>{t('page.alerts.probeCard')}</p>
                        <div className='mt-2 flex flex-wrap gap-2'>
                            {summary.counts.failure + summary.counts.send_failed > 0 && (
                                <Badge tone='danger' icon='error'>
                                    {t('value.probeStatus.failure')}:{' '}
                                    {summary.counts.failure + summary.counts.send_failed}
                                </Badge>
                            )}
                            {summary.counts.delayed > 0 && (
                                <Badge tone='warning' icon='hourglass_bottom'>
                                    {t('value.probeStatus.delayed')}: {summary.counts.delayed}
                                </Badge>
                            )}
                            {summary.counts.failure + summary.counts.send_failed + summary.counts.delayed === 0 && (
                                <Badge
                                    tone={summary.enabled ? 'success' : 'neutral'}
                                    icon={summary.enabled ? 'check_circle' : 'remove_circle_outline'}
                                >
                                    {summary.enabled ? t('page.dashboard.probeOk') : t('page.dashboard.probeOff')}
                                </Badge>
                            )}
                        </div>
                    </div>
                    <Icon name='chevron_right' className='self-center text-muted' />
                </Link>
            )}
            {loading ? (
                <LoadingBlock />
            ) : error || !data ? (
                <ErrorState message={t(error?.key ?? 'system.internal', error?.params)} onRetry={() => void reload()} />
            ) : (
                <MailboxPicker mailboxes={data} linkTo={mb => `/alerts/${mb.id}`} highlight='open' />
            )}
        </PageContainer>
    );
}
