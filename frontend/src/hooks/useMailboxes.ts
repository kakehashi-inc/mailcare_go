import { listMailboxes } from '../api/client';
import type { MailboxDTO } from '../types';
import { useAsync } from './useAsync';

/** The mailbox list, for selects and pickers. */
export function useMailboxes(): {
    mailboxes: MailboxDTO[];
    loading: boolean;
    error: unknown;
    reload: () => Promise<void>;
} {
    const { data, loading, error, reload } = useAsync(listMailboxes, []);
    return { mailboxes: data ?? [], loading, error, reload };
}
