import type { TFunction } from 'i18next';
import { IGNORE_REASONS, RESOLVE_ACTIONS, type GroupState, type IgnoreReason, type ResolveAction } from '../types';

/**
 * Translated label of the code chosen with a state (what was done for resolved, why for ignored); "-" when
 * none was recorded, an unknown code (newer server) as is.
 */
export function stateReasonLabel(state: GroupState, reason: string | null, t: TFunction): string {
    if (!reason) return '-';
    if (state === 'resolved' && (RESOLVE_ACTIONS as readonly string[]).includes(reason)) {
        return t(`value.resolveAction.${reason as ResolveAction}`);
    }
    if (state === 'ignored' && (IGNORE_REASONS as readonly string[]).includes(reason)) {
        return t(`value.ignoreReason.${reason as IgnoreReason}`);
    }
    return reason;
}

/** The label of the field that holds the code chosen with a state. */
export function stateReasonField(state: GroupState, t: TFunction): string {
    return state === 'resolved' ? t('field.group.resolveAction') : t('field.group.ignoreReason');
}
