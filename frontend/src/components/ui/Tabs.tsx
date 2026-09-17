import { useRef, type KeyboardEvent, type ReactNode } from 'react';

export interface TabItem<K extends string> {
    key: K;
    label: ReactNode;
    icon?: string;
    count?: number;
}

interface TabsProps<K extends string> {
    tabs: TabItem<K>[];
    value: K;
    onChange: (key: K) => void;
    label: string;
}

/** Accessible tab list with arrow-key navigation. Panels are rendered by the caller. */
export function Tabs<K extends string>({ tabs, value, onChange, label }: TabsProps<K>) {
    const refs = useRef<(HTMLButtonElement | null)[]>([]);

    function onKeyDown(e: KeyboardEvent<HTMLButtonElement>, index: number) {
        let next = -1;
        if (e.key === 'ArrowRight') next = (index + 1) % tabs.length;
        else if (e.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length;
        else if (e.key === 'Home') next = 0;
        else if (e.key === 'End') next = tabs.length - 1;
        if (next >= 0) {
            e.preventDefault();
            onChange(tabs[next].key);
            refs.current[next]?.focus();
        }
    }

    return (
        <div role='tablist' aria-label={label} className='flex flex-wrap gap-1 border-b border-line'>
            {tabs.map((tab, i) => {
                const selected = tab.key === value;
                return (
                    <button
                        key={tab.key}
                        ref={el => {
                            refs.current[i] = el;
                        }}
                        type='button'
                        role='tab'
                        id={`tab-${tab.key}`}
                        aria-selected={selected}
                        aria-controls={`tabpanel-${tab.key}`}
                        tabIndex={selected ? 0 : -1}
                        onClick={() => onChange(tab.key)}
                        onKeyDown={e => onKeyDown(e, i)}
                        className={`-mb-px inline-flex min-h-tap shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-base font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent ${
                            selected
                                ? 'border-accent text-accent'
                                : 'border-transparent text-muted hover:border-line hover:text-ink'
                        }`}
                    >
                        {tab.icon && (
                            <span className='material-icons-outlined text-[18px]' aria-hidden='true'>
                                {tab.icon}
                            </span>
                        )}
                        {tab.label}
                        {tab.count !== undefined && (
                            <span
                                className={`rounded-full px-2 py-0.5 text-xs font-semibold ${
                                    selected ? 'bg-accent-soft text-accent' : 'bg-well text-muted'
                                }`}
                            >
                                {tab.count}
                            </span>
                        )}
                    </button>
                );
            })}
        </div>
    );
}

export function TabPanel({ id, active, children }: { id: string; active: boolean; children: ReactNode }) {
    if (!active) return null;
    return (
        <div role='tabpanel' id={`tabpanel-${id}`} aria-labelledby={`tab-${id}`} className='pt-4'>
            {children}
        </div>
    );
}
