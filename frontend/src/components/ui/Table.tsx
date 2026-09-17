import type { ReactNode } from 'react';
import { useMediaQuery } from '../../hooks/useMediaQuery';
import { BREAKPOINT_MD } from '../../constants';

export interface Column<T> {
    key: string;
    header: ReactNode;
    /** Renders the cell. */
    cell: (row: T) => ReactNode;
    /** Extra classes for the <td>/<th> in table mode. */
    className?: string;
    /** In card mode: hide the label (e.g. for a title cell that speaks for itself). */
    hideLabelInCard?: boolean;
    /** In card mode: render as the card heading. */
    primary?: boolean;
    /** Hide in card mode entirely. */
    hideInCard?: boolean;
}

interface TableProps<T> {
    columns: Column<T>[];
    rows: T[];
    rowKey: (row: T) => string | number;
    /** Optional: makes the whole row/card focusable and clickable. */
    onRowClick?: (row: T) => void;
    rowAriaLabel?: (row: T) => string;
    caption?: string;
    emptyState?: ReactNode;
    /** Force card layout regardless of width. */
    cards?: boolean;
    dense?: boolean;
}

/**
 * Data table that turns into a stack of cards below the "md" breakpoint so
 * nothing needs horizontal scrolling on a phone.
 */
export function Table<T>({
    columns,
    rows,
    rowKey,
    onRowClick,
    rowAriaLabel,
    caption,
    emptyState,
    cards,
    dense,
}: TableProps<T>) {
    const narrow = useMediaQuery(`(max-width: ${BREAKPOINT_MD - 1}px)`);
    const asCards = cards || narrow;

    if (rows.length === 0 && emptyState) {
        return <>{emptyState}</>;
    }

    const clickable = Boolean(onRowClick);

    if (asCards) {
        const primary = columns.find(c => c.primary);
        const rest = columns.filter(c => !c.primary && !c.hideInCard);
        return (
            <ul className='flex flex-col gap-3' aria-label={caption}>
                {rows.map(row => (
                    <li
                        key={rowKey(row)}
                        className={`rounded-xl border border-line bg-surface p-4 shadow-sm ${
                            clickable ? 'cursor-pointer hover:border-accent focus-within:border-accent' : ''
                        }`}
                        onClick={clickable ? () => onRowClick?.(row) : undefined}
                    >
                        {primary && (
                            <div className='mb-2 text-base font-semibold text-ink'>
                                {clickable ? (
                                    <button
                                        type='button'
                                        className='flex min-h-tap w-full items-center rounded text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                                        aria-label={rowAriaLabel?.(row)}
                                        onClick={e => {
                                            e.stopPropagation();
                                            onRowClick?.(row);
                                        }}
                                    >
                                        {primary.cell(row)}
                                    </button>
                                ) : (
                                    primary.cell(row)
                                )}
                            </div>
                        )}
                        <dl className='grid grid-cols-1 gap-x-4 gap-y-1 text-sm sm:grid-cols-2'>
                            {rest.map(col => (
                                <div key={col.key} className='flex min-w-0 flex-col gap-0.5'>
                                    {!col.hideLabelInCard && <dt className='text-muted'>{col.header}</dt>}
                                    <dd className='min-w-0 break-words text-ink'>{col.cell(row)}</dd>
                                </div>
                            ))}
                        </dl>
                    </li>
                ))}
            </ul>
        );
    }

    return (
        <div className='overflow-x-auto rounded-xl border border-line bg-surface shadow-sm'>
            <table className='w-full border-collapse text-left text-base'>
                {caption && <caption className='sr-only'>{caption}</caption>}
                <thead className='bg-well text-sm text-muted'>
                    <tr>
                        {columns.map(col => (
                            <th key={col.key} scope='col' className={`px-3 py-2 font-medium ${col.className ?? ''}`}>
                                {col.header}
                            </th>
                        ))}
                    </tr>
                </thead>
                <tbody>
                    {rows.map(row => (
                        <tr
                            key={rowKey(row)}
                            tabIndex={clickable ? 0 : undefined}
                            aria-label={clickable ? rowAriaLabel?.(row) : undefined}
                            onClick={clickable ? () => onRowClick?.(row) : undefined}
                            onKeyDown={
                                clickable
                                    ? e => {
                                          if (e.key === 'Enter' || e.key === ' ') {
                                              e.preventDefault();
                                              onRowClick?.(row);
                                          }
                                      }
                                    : undefined
                            }
                            className={`border-t border-line align-top ${
                                clickable
                                    ? 'cursor-pointer hover:bg-well focus:bg-well focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent'
                                    : ''
                            }`}
                        >
                            {columns.map(col => (
                                <td key={col.key} className={`px-3 ${dense ? 'py-2' : 'py-3'} ${col.className ?? ''}`}>
                                    {col.cell(row)}
                                </td>
                            ))}
                        </tr>
                    ))}
                </tbody>
            </table>
        </div>
    );
}
