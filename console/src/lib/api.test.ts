import { afterEach, describe, expect, it } from 'vitest';
import { api, ApiError, describeError, setTransport } from './api';
import { auth } from './auth.svelte';

const json = (status: number, body: unknown) =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
afterEach(() => auth.signOut());

describe('api client', () => {
    it('sends the bearer token and nothing in the URL', async () => {
        let seen: { url: string; init: RequestInit } | undefined;
        setTransport(async (url, init) => ((seen = { url, init }), json(200, { items: [], used: 0, warnings: [] })));
        auth.signIn(' tok ');
        await api.agents();
        expect((seen?.init.headers as Record<string, string>)['Authorization']).toBe('Bearer tok');
        expect(seen?.url).toBe('/api/v1/agents');
        expect(seen?.init.credentials).toBe('omit');
    });
    it('signs out on 401', async () => {
        setTransport(async () => json(401, { error: { code: 'unauthenticated', message: 'no' } }));
        auth.signIn('x');
        await expect(api.session()).rejects.toMatchObject({ status: 401 });
        expect(auth.signedIn).toBe(false);
    });
    it('keeps the server sentence on 409, and turns a network failure into advice', async () => {
        setTransport(async () => json(409, { error: { code: 'conflict', message: 'changed.' } }));
        const e = await api.update('a', { name: 'a', spec: { model: 'm', instructions: 'x' } }).catch((x) => x);
        expect(e).toBeInstanceOf(ApiError);
        expect(describeError('save', 'a', e)).toBe('Could not save a: the console API returned 409. changed.');
        setTransport(async () => {
            throw new TypeError('failed');
        });
        const n = await api.agents().catch((x) => x);
        expect(n.network).toBe(true);
        expect(describeError('read', 'the agents', n)).toMatch(/port-forward/);
    });
    it('explains 403', () => {
        expect(describeError('save', 'a', new ApiError(403, 'forbidden', 'This console is read-only.'))).toContain(
            'returned 403. This console is read-only.',
        );
    });
});
