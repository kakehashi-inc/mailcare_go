import type { TFunction } from 'i18next';
import type { BadgeTone } from '../components/ui/Badge';
import { BOUNCE_CATEGORIES, type BounceCategory, type GroupDTO } from '../types';

export function isBounceCategory(value: string): value is BounceCategory {
    return (BOUNCE_CATEGORIES as readonly string[]).includes(value);
}

const ICON: Record<BounceCategory, string> = {
    ip_blocked: 'block',
    rate_limited: 'speed',
    auth_failure: 'gpp_bad',
    sender_blocked: 'unsubscribe',
    content_rejected: 'report',
    message_too_large: 'attach_file',
    server_config: 'build',
    unknown_failure: 'help_outline',
    user_unknown: 'no_accounts',
    mailbox_full: 'storage',
    mailbox_disabled: 'person_off',
    domain_not_found: 'domain_disabled',
    delivery_delay: 'hourglass_top',
};

const TONE: Record<BounceCategory, BadgeTone> = {
    ip_blocked: 'danger',
    rate_limited: 'warning',
    auth_failure: 'danger',
    sender_blocked: 'danger',
    content_rejected: 'warning',
    message_too_large: 'warning',
    server_config: 'danger',
    unknown_failure: 'warning',
    user_unknown: 'neutral',
    mailbox_full: 'neutral',
    mailbox_disabled: 'neutral',
    domain_not_found: 'neutral',
    delivery_delay: 'info',
};

export function categoryIcon(category: string): string {
    return isBounceCategory(category) ? ICON[category] : 'label';
}

export function categoryTone(category: string): BadgeTone {
    return isBounceCategory(category) ? TONE[category] : 'neutral';
}

/** Translated category name; an unknown category (newer server) is shown as is. */
export function categoryLabel(category: string, t: TFunction): string {
    return isBounceCategory(category) ? t(`category.${category}.label`) : category;
}

/** What the administrator should do about this category, or "" when unknown. */
export function categoryDescription(category: string, t: TFunction): string {
    return isBounceCategory(category) ? t(`category.${category}.description`) : '';
}

/**
 * Localized one-line title of a group, built from the category, the action unit
 * and the authority, e.g. "Sending IP blocked: 203.0.113.5 (spamhaus.org)".
 * The authority of an unknown failure is the status code plus the diagnostic
 * template, which the technical line shows already, so it is left out here.
 * Without an action unit the label is followed by the authority or the
 * recipient domain, e.g. "Content rejected (broken.example.net)". The raw
 * internal title is used only when the category itself is unknown.
 */
export function groupHeadline(
    group: Pick<GroupDTO, 'category' | 'unit_value' | 'authority' | 'recipient_domain' | 'title'>,
    t: TFunction
): string {
    if (!isBounceCategory(group.category)) return group.title;
    const label = categoryLabel(group.category, t);
    const authority = group.category === 'unknown_failure' ? '' : group.authority;
    if (group.unit_value) {
        return authority
            ? t('group.headlineWithAuthority', { label, unit: group.unit_value, authority })
            : t('group.headline', { label, unit: group.unit_value });
    }
    const where = authority || group.recipient_domain;
    return where ? t('group.headlineNoUnit', { label, where }) : label;
}
