import { describe, expect, it } from 'vitest';
import {
    bucketSentences,
    byteLength,
    counterText,
    draftFromSpec,
    emptyDraft,
    featureState,
    fieldOfSentence,
    modelOptions,
    nameProblem,
    nearLimit,
    profileOptions,
    scanSecrets,
    skillStates,
    specFromDraft,
    toolRows,
    addedPrompts,
} from './draft';
import { platform } from './fixtures';

describe('draft', () => {
    it('starts from the first allowed model and a required profile', () => {
        const d = emptyDraft(platform());
        expect(d.model).toBe('claude-haiku');
        expect(d.toolProfile).toBe('lookup');
    });
    it('round-trips a spec and leaves out what is unset', () => {
        const spec = {
            model: 'm',
            instructions: 'x',
            toolProfile: 'lookup',
            tools: ['a'],
            knowledge: { enabled: true },
            notify: { enabled: true, destinations: ['ops'] },
            memory: { enabled: true, ttlDays: 5 },
            skills: [{ name: 's', version: '2' }, { name: 't' }],
            suspend: true,
            egress: { allow: ['https://a.org'] },
        };
        expect(specFromDraft(draftFromSpec('n', spec))).toEqual(spec);
        expect(specFromDraft({ ...emptyDraft(undefined), model: 'm', instructions: 'x' })).toEqual({
            model: 'm',
            instructions: 'x',
        });
    });
    it('counts bytes, not characters', () => {
        expect(byteLength('é')).toBe(2);
        expect(counterText(1284, 16384, 'bytes')).toBe('1,284 / 16,384 bytes');
        expect(nearLimit(14800, 16384)).toBe(true);
        expect(nearLimit(100, 16384)).toBe(false);
    });
    it('checks names', () => {
        const p = platform();
        expect(nameProblem('Bad', p)).toMatch(/lower case/);
        expect(nameProblem('assistant', p)).toBe('assistant is reserved (chart agents and platform services own it)');
        expect(nameProblem('new', p)).toMatch(/page of the console/);
        expect(nameProblem('x'.repeat(41), p)).toMatch(/too long/);
        expect(nameProblem('good-name', p)).toBe('');
    });
});

describe('policy options', () => {
    it('disables a model and a profile the policy forbids, with the reason', () => {
        const p = platform();
        p.models.push('other');
        expect(modelOptions(p).find((o) => o.value === 'other')).toMatchObject({
            disabled: true,
            reason: 'Not allowed by the platform policy',
        });
        expect(profileOptions(p)[0]).toMatchObject({
            value: '',
            disabled: true,
            reason: 'The platform policy requires a toolProfile',
        });
        p.policy.requireToolProfile = false;
        expect(profileOptions(p)[0]?.disabled).toBe(false);
    });
    it('explains why knowledge, notify and memory are off', () => {
        const p = platform();
        p.policy.allowKnowledge = false;
        expect(featureState(p, 'knowledge').reason).toBe('Not allowed by the platform policy');
        p.notify.available = false;
        expect(featureState(p, 'notify').reason).toBe('Notify is not installed on this platform');
        p.memory.expiryEnabled = false;
        expect(featureState(p, 'memory').reason).toMatch(/expiry job is off/);
        expect(featureState(platform(), 'memory').disabled).toBe(false);
    });
    it('lists skills the policy forbids as disabled', () => {
        const p = platform();
        p.skills.push({ name: 'extra', ref: 'x' });
        expect(skillStates(p).find((s) => s.name === 'extra')?.disabled).toBe(true);
    });
    it('shows tools of the profile, and foreign ones as refused rows', () => {
        const rows = toolRows(platform(), 'lookup', ['osv_query_vulnerability', 'knowledge_kb_get_document']);
        expect(rows.find((r) => r.name === 'osv_query_vulnerability')).toMatchObject({
            prefix: 'osv_',
            rest: 'query_vulnerability',
            foreign: false,
        });
        expect(rows.find((r) => r.name === 'knowledge_kb_get_document')?.foreign).toBe(true);
        expect(rows.find((r) => r.code)?.name).toBe('execute_tool_script');
    });
    it('lists the prompts the draft adds', () => {
        const d = { ...emptyDraft(platform()), knowledge: true, memoryEnabled: true };
        expect(addedPrompts(platform(), d).map((x) => x.title)).toEqual(['knowledge', 'memory']);
    });
});

describe('secrets and violations', () => {
    it('finds something shaped like a secret and says where', () => {
        const t = `before Bearer ${'a'.repeat(30)} after`;
        const m = scanSecrets(t);
        expect(m[0]).toMatchObject({ name: 'bearer_token', start: 7 });
        expect(scanSecrets('plain words')).toEqual([]);
    });
    it('maps controller sentences to fields word for word', () => {
        const a =
            'the definition lists tool "knowledge_kb_get_document" which is not in profile "lookup" (the profile offers: x)';
        const b = 'egress.allow entry "https://services.nvd.nist.gov" is not matched by the platform policy';
        expect(fieldOfSentence(a)).toBe('tools');
        expect(fieldOfSentence(b)).toBe('egress.allow');
        expect(fieldOfSentence('memory.ttlDays 90 is above the platform maximum of 30')).toBe('memory.ttlDays');
        expect(fieldOfSentence('something unknown')).toBeNull();
        expect(bucketSentences([a, b]).get('tools')).toEqual([a]);
    });
});
