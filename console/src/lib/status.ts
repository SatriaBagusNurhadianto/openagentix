// Status words, condition views and time formatting (UI-SPEC sections 3 and 7).
import { CONDITION_ORDER, type Condition, type State } from './types';

export function stateWord(state: State, deleting = false): string {
    if (deleting) return 'Deleting';
    return { ready: 'Ready', pending: 'Pending', refused: 'Refused', error: 'Failed', paused: 'Paused' }[state];
}

export type CondKind = 'ready' | 'pending' | 'refused' | 'error' | 'none';

export interface CondView {
    type: string;
    kind: CondKind;
    /** True, False, Unknown, or "Not reached" when the controller has not got that far. */
    status: string;
    reason: string;
    message: string;
    time: string;
}

const ERROR_REASONS = new Set(['ApplyFailed', 'RenderFailed', 'CheckFailed', 'PruneFailed', 'NameConflict']);

/** The four conditions in the fixed order, each with the colour kind it takes in the rail. */
export function conditionViews(conds: Condition[]): CondView[] {
    const by = new Map(conds.map((c) => [c.type, c]));
    const accepted = by.get('Accepted');
    const refused = accepted?.status === 'False';
    return CONDITION_ORDER.map((type): CondView => {
        const c = by.get(type);
        const base = { type, reason: c?.reason ?? '', message: c?.message ?? '', time: c?.lastTransitionTime ?? '' };
        if (!c) return { ...base, kind: 'none', status: 'Not reached' };
        const none = (): CondView => ({
            ...base,
            kind: 'none',
            status: c.status === 'Unknown' ? 'Not reached' : c.status,
        });
        if (c.reason === 'NotAccepted' || (refused && type !== 'Accepted' && c.status !== 'True')) return none();
        if (c.status === 'True') return { ...base, kind: 'ready', status: 'True' };
        if (c.status === 'Unknown') return { ...base, kind: 'pending', status: 'Unknown' };
        if (type === 'Accepted') return { ...base, kind: 'refused', status: 'False' };
        if (type === 'KeyReady') return { ...base, kind: 'error', status: 'False' };
        return { ...base, kind: ERROR_REASONS.has(c.reason) ? 'error' : 'pending', status: 'False' };
    });
}

/** "Applied: False, ApplyFailed", the accessible name of a rail cell or pip. */
export function railName(v: CondView): string {
    return `${v.type}: ${v.status}${v.reason && v.kind !== 'none' ? `, ${v.reason}` : ''}`;
}

/** Relative within 24 hours ("40 s ago"), an absolute local date after that ("25 Nov 2026, 08:00"). */
export function formatTime(iso: string | undefined, now: Date = new Date()): string {
    if (!iso) return '';
    const t = new Date(iso);
    if (Number.isNaN(t.getTime())) return '';
    const s = Math.round((now.getTime() - t.getTime()) / 1000);
    if (s < 0) return 'just now';
    if (s < 60) return `${s} s ago`;
    if (s < 3600) return `${Math.floor(s / 60)} min ago`;
    if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
    if (s < 86400 * 2) return `${Math.floor(s / 86400)} d ago`;
    return t
        .toLocaleString('en-GB', {
            day: 'numeric',
            month: 'short',
            year: 'numeric',
            hour: '2-digit',
            minute: '2-digit',
            hour12: false,
        })
        .replace(' at ', ', ');
}

/** The absolute time for a tooltip: local with UTC. */
export function absoluteTime(iso: string | undefined): string {
    if (!iso) return '';
    const t = new Date(iso);
    if (Number.isNaN(t.getTime())) return '';
    return `${t.toLocaleString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })} local · ${t.toISOString().replace('T', ' ').slice(0, 19)} UTC`;
}

export function memoryTag(m: { enabled: boolean; ttlDays?: number } | undefined): string | null {
    if (!m?.enabled) return null;
    return m.ttlDays ? `memory ${m.ttlDays}d` : 'memory';
}

/** Is the status still moving? The builder and the registry poll while this is true. */
export function unsettled(
    a: { state: State; deleting: boolean; generation: number; source: string },
    observedGeneration?: number,
): boolean {
    if (a.source === 'chart') return false;
    if (a.deleting || a.state === 'pending') return true;
    if (observedGeneration !== undefined && observedGeneration < a.generation) return true;
    return false;
}
