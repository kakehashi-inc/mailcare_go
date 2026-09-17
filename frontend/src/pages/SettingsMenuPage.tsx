import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useAuth } from '../auth/AuthProvider';
import { Badge } from '../components/ui/Badge';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { useDocumentTitle } from '../hooks/useDocumentTitle';

export function SettingsMenuPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.settings'));
    const { isAdmin } = useAuth();

    const entries = [
        {
            to: '/settings/general',
            icon: 'tune',
            label: t('nav.settingsGeneral'),
            description: t('settings.menu.general'),
            admin: true,
        },
        {
            to: '/settings/mailboxes',
            icon: 'alternate_email',
            label: t('nav.settingsMailboxes'),
            description: t('settings.menu.mailboxes'),
            admin: true,
        },
        {
            to: '/settings/users',
            icon: 'group',
            label: t('nav.settingsUsers'),
            description: t('settings.menu.users'),
            admin: true,
        },
        {
            to: '/settings/tokens',
            icon: 'key',
            label: t('nav.settingsTokens'),
            description: t('settings.menu.tokens'),
            admin: false,
        },
        {
            to: '/settings/account',
            icon: 'manage_accounts',
            label: t('nav.settingsAccount'),
            description: t('settings.menu.account'),
            admin: false,
        },
    ].filter(entry => isAdmin || !entry.admin);

    return (
        <PageContainer>
            <PageHeader title={t('nav.settings')} description={t('settings.description')} />
            <ul className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                {entries.map(entry => (
                    <li key={entry.to}>
                        <Link
                            to={entry.to}
                            className='flex h-full gap-4 rounded-xl border border-line bg-surface p-4 shadow-sm transition-colors hover:border-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                        >
                            <Icon name={entry.icon} className='text-[32px] text-accent' />
                            <div className='min-w-0 flex-1'>
                                <div className='flex flex-wrap items-center gap-2'>
                                    <span className='text-lg font-semibold text-ink'>{entry.label}</span>
                                    {entry.admin && (
                                        <Badge tone={isAdmin ? 'accent' : 'neutral'} icon='admin_panel_settings'>
                                            {t('settings.adminOnly')}
                                        </Badge>
                                    )}
                                </div>
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
