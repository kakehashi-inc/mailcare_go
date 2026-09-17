import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';

/** Sets "<title> - MailCare" as the document title while the page is mounted. */
export function useDocumentTitle(title: string | undefined): void {
    const { t } = useTranslation();
    useEffect(() => {
        const app = t('app.title');
        document.title = title ? `${title} - ${app}` : app;
        return () => {
            document.title = app;
        };
    }, [title, t]);
}
