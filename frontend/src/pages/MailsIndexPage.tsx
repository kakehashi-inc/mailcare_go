import { useTranslation } from 'react-i18next';
import { listMailboxes } from '../api/client';
import { MailboxPicker } from '../components/domain/MailboxPicker';
import { ErrorState } from '../components/ui/ErrorState';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';

export function MailsIndexPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.mails'));
    const { data, error, loading, reload } = useAsync(() => listMailboxes(true), []);

    return (
        <PageContainer wide>
            <PageHeader title={t('layout.nav.mails')} description={t('page.mails.chooseMailbox')} />
            {loading ? (
                <LoadingBlock />
            ) : error || !data ? (
                <ErrorState message={t(error?.key ?? 'system.internal', error?.params)} onRetry={() => void reload()} />
            ) : (
                <MailboxPicker mailboxes={data} linkTo={mb => `/mails/${mb.id}`} highlight='messages' />
            )}
        </PageContainer>
    );
}
