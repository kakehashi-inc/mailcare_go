import type { ReactNode } from 'react';

export interface DescriptionItem {
    label: ReactNode;
    value: ReactNode;
    /** Span both columns on wide screens. */
    wide?: boolean;
}

/** Key/value grid: one column on phones, two on tablets and up. */
export function DescriptionList({ items, columns = 2 }: { items: DescriptionItem[]; columns?: 1 | 2 | 3 }) {
    const cols = columns === 1 ? 'sm:grid-cols-1' : columns === 3 ? 'sm:grid-cols-3' : 'sm:grid-cols-2';
    return (
        <dl className={`grid grid-cols-1 gap-x-6 gap-y-3 ${cols}`}>
            {items.map((item, i) => (
                <div key={i} className={`min-w-0 ${item.wide ? 'sm:col-span-full' : ''}`}>
                    <dt className='text-sm text-muted'>{item.label}</dt>
                    <dd className='mt-0.5 break-words text-base text-ink'>{item.value ?? '-'}</dd>
                </div>
            ))}
        </dl>
    );
}
