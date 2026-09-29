import { useTranslation } from 'react-i18next';
import { Button } from './Button';

interface PaginationProps {
    page: number;
    perPage: number;
    total: number;
    onChange: (page: number) => void;
}

/**
 * Page navigation: the range shown, then icon-only buttons to the first, previous, next and last page
 * around the page number. Each button is named for assistive tech and as a tooltip.
 */
export function Pagination({ page, perPage, total, onChange }: PaginationProps) {
    const { t } = useTranslation();
    const pages = Math.max(1, Math.ceil(total / perPage));
    const current = Math.min(Math.max(1, page), pages);
    const from = total === 0 ? 0 : Math.min(total, (current - 1) * perPage + 1);
    const to = Math.min(total, current * perPage);
    const pageButton = (icon: string, label: string, target: number, disabled: boolean) => (
        <Button
            size='sm'
            icon={icon}
            className='min-w-tap px-2'
            disabled={disabled}
            onClick={() => onChange(target)}
            aria-label={label}
            title={label}
        />
    );
    return (
        <nav aria-label={t('component.pagination.label')} className='flex flex-wrap items-center justify-between gap-3'>
            <p className='text-sm text-muted'>{t('component.pagination.range', { from, to, total })}</p>
            <div className='flex items-center gap-2'>
                {pageButton('first_page', t('component.pagination.first'), 1, current <= 1)}
                {pageButton('chevron_left', t('component.pagination.prev'), current - 1, current <= 1)}
                <span className='text-sm text-muted' aria-current='page'>
                    {t('component.pagination.page', { page: current, pages })}
                </span>
                {pageButton('chevron_right', t('component.pagination.next'), current + 1, current >= pages)}
                {pageButton('last_page', t('component.pagination.last'), pages, current >= pages)}
            </div>
        </nav>
    );
}
