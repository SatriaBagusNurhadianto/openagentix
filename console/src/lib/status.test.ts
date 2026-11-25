import { describe, expect, it } from 'vitest';
import { conditionViews, formatTime, railName, stateWord, unsettled } from './status';
import { agent } from './fixtures';

const c = (type: string, status: 'True' | 'False' | 'Unknown', reason = '') => ({ type, status, reason });

describe('status', () => {
    it('uses the words of the contract', () => {
        expect(['ready', 'pending', 'refused', 'error'].map((s) => stateWord(s as 'ready'))).toEqual([
            'Ready',
            'Pending',
            'Refused',
            'Failed',
        ]);
        expect(stateWord('pending', true)).toBe('Deleting');
    });
    it('shows where a refused definition stopped', () => {
        const v = conditionViews([
            c('Accepted', 'False', 'PolicyViolation'),
            c('KeyReady', 'Unknown', 'NotAccepted'),
            c('Applied', 'False', 'NotAccepted'),
            c('Ready', 'False', 'NotAccepted'),
        ]);
        expect(v.map((x) => x.kind)).toEqual(['refused', 'none', 'none', 'none']);
        expect(railName(v[0]!)).toBe('Accepted: False, PolicyViolation');
    });
    it('tells an error from a wait', () => {
        const base = [c('Accepted', 'True'), c('KeyReady', 'True')];
        expect(
            conditionViews([...base, c('Applied', 'False', 'ApplyFailed'), c('Ready', 'False', 'NotApplied')]).map(
                (x) => x.kind,
            ),
        ).toEqual(['ready', 'ready', 'error', 'pending']);
        expect(conditionViews([...base, c('Applied', 'True'), c('Ready', 'False', 'AgentPending')])[3]?.kind).toBe(
            'pending',
        );
        expect(conditionViews([]).every((x) => x.kind === 'none')).toBe(true);
    });
    it('formats times relative, then absolute', () => {
        const now = new Date('2026-11-25T12:00:00Z');
        expect(formatTime('2026-11-25T11:59:20Z', now)).toBe('40 s ago');
        expect(formatTime('2026-11-25T08:59:00Z', now)).toBe('3 h ago');
        expect(formatTime('2026-11-20T08:00:00Z', now)).toMatch(/2026/);
    });
    it('polls only what is not settled', () => {
        expect(unsettled(agent({ state: 'pending' }))).toBe(true);
        expect(unsettled(agent({ state: 'ready' }), 1)).toBe(false);
        expect(unsettled(agent({ state: 'ready', generation: 4 }), 3)).toBe(true);
        expect(unsettled(agent({ state: 'pending', source: 'chart' }))).toBe(false);
    });
});
