import { useTranslation } from 'react-i18next';
import { Button } from './Button';

interface PaginationProps {
    page: number;
    perPage: number;
    total: number;
    onChange: (page: number) => void;
}

export function Pagination({ page, perPage, total, onChange }: PaginationProps) {
    const { t } = useTranslation();
    const pages = Math.max(1, Math.ceil(total / perPage));
    const current = Math.min(Math.max(1, page), pages);
    const from = total === 0 ? 0 : Math.min(total, (current - 1) * perPage + 1);
    const to = Math.min(total, current * perPage);
    return (
        <nav aria-label={t('pagination.label')} className='flex flex-wrap items-center justify-between gap-3'>
            <p className='text-sm text-muted'>{t('pagination.range', { from, to, total })}</p>
            <div className='flex items-center gap-2'>
                <Button size='sm' icon='chevron_left' disabled={current <= 1} onClick={() => onChange(current - 1)}>
                    {t('pagination.prev')}
                </Button>
                <span className='text-sm text-muted' aria-current='page'>
                    {t('pagination.page', { page: current, pages })}
                </span>
                <Button size='sm' disabled={current >= pages} onClick={() => onChange(current + 1)}>
                    {t('pagination.next')}
                    <span className='material-icons-outlined text-[20px]' aria-hidden='true'>
                        chevron_right
                    </span>
                </Button>
            </div>
        </nav>
    );
}
