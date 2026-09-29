import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Badge } from '../components/ui/Badge';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { useDocumentTitle } from '../hooks/useDocumentTitle';

export function SettingsMenuPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.settings'));

    const entries = [
        {
            to: '/settings/general',
            icon: 'tune',
            label: t('layout.nav.settingsGeneral'),
            description: t('page.settings.menu.general'),
        },
        {
            to: '/settings/notifications',
            icon: 'mark_email_unread',
            label: t('layout.nav.settingsNotifications'),
            description: t('page.settings.menu.notifications'),
        },
        {
            to: '/settings/mailboxes',
            icon: 'alternate_email',
            label: t('layout.nav.settingsMailboxes'),
            description: t('page.settings.menu.mailboxes'),
        },
        {
            to: '/settings/users',
            icon: 'group',
            label: t('layout.nav.settingsUsers'),
            description: t('page.settings.menu.users'),
        },
        {
            to: '/settings/tokens',
            icon: 'key',
            label: t('layout.nav.settingsTokens'),
            description: t('page.settings.menu.tokens'),
        },
    ];

    return (
        <PageContainer>
            <PageHeader
                title={
                    <span className='inline-flex flex-wrap items-center gap-3'>
                        {t('layout.nav.settings')}
                        <Badge tone='accent' icon='admin_panel_settings'>
                            {t('page.settings.adminOnly')}
                        </Badge>
                    </span>
                }
                description={t('page.settings.description')}
            />
            <ul className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                {entries.map(entry => (
                    <li key={entry.to}>
                        <Link
                            to={entry.to}
                            className='flex h-full gap-4 rounded-xl border border-line bg-surface p-4 shadow-sm transition-colors hover:border-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                        >
                            <Icon name={entry.icon} className='text-[32px] text-accent' />
                            <div className='min-w-0 flex-1'>
                                <span className='block text-lg font-semibold text-ink'>{entry.label}</span>
                                <p className='mt-1 text-sm text-muted'>{entry.description}</p>
                            </div>
                            <Icon name='chevron_right' className='self-center text-muted' />
                        </Link>
                    </li>
                ))}
            </ul>
        </PageContainer>
    );
}
