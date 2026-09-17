import { useTranslation } from 'react-i18next';
import { listMailboxes } from '../api/client';
import { MailboxPicker } from '../components/domain/MailboxPicker';
import { ErrorState } from '../components/ui/ErrorState';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { errorMessage } from '../utils/errors';

export function AlertsIndexPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.alerts'));
    const { data, error, loading, reload } = useAsync(() => listMailboxes(true), []);

    return (
        <PageContainer wide>
            <PageHeader title={t('nav.alerts')} description={t('alerts.chooseMailbox')} />
            {loading ? (
                <LoadingBlock />
            ) : error || !data ? (
                <ErrorState message={errorMessage(error, t)} onRetry={() => void reload()} />
            ) : (
                <MailboxPicker mailboxes={data} linkTo={mb => `/alerts/${mb.id}`} highlight='open' />
            )}
        </PageContainer>
    );
}
