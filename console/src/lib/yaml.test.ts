import { parse } from 'yaml';
import { describe, expect, it } from 'vitest';
import { renderSpecYaml, scalar } from './yaml';
import type { Spec } from './types';

// strings a YAML writer must get right: indicators, quotes, escapes, lookalikes of other types and the whitespace a
// block scalar must keep
const NASTY = [
    'a: b',
    '#hash',
    'a #b',
    '- dash',
    '-',
    '? q',
    'key: value: more',
    "'single'",
    '"double"',
    'say "hi"',
    'back\\slash',
    'tab\tinside',
    'unicode é ü 日本語 🙂',
    'trailing space ',
    '  leading space',
    'line one\nline two',
    'line one \nline two  \n',
    'ends with newline\n',
    '\nstarts with newline',
    'two\n\nblank lines',
    'no\nfinal newline',
    'yes',
    'No',
    'off',
    'null',
    '~',
    '3',
    '3.14',
    '0x1F',
    '1e3',
    '2026-11-25',
    '12:30',
    '@at',
    '`tick',
    '!tag',
    '&anchor',
    '*alias',
    '%percent',
    '{flow}',
    '[seq]',
    'a, b',
    '|',
    '>',
    '=',
    '<<',
    ' ',
    '\t',
    'colon:',
    ':colon',
    'http://x.example/a?b=c#d',
    '\n',
    '\n\n',
    '  \n  ',
    'last line has spaces   ',
    'x\r\ny',
    'nul\u0000byte',
    'bell\u0007',
    'line sep here',
    'y',
    'n',
];

function specWith(s: string): Spec {
    return {
        description: s,
        model: s,
        instructions: s,
        toolProfile: s,
        tools: [s, 'b'],
        knowledge: { enabled: true, collections: [s] },
        notify: { enabled: true, destinations: [s] },
        skills: [{ name: s, version: s }],
        egress: { allow: [s] },
    };
}

describe('the draft YAML parses back to the same data', () => {
    for (const s of NASTY) {
        it(JSON.stringify(s), () => {
            const doc = parse(renderSpecYaml(s, 'ns', specWith(s))) as {
                metadata: { name: string };
                spec: Record<string, unknown>;
            };
            const sp = doc.spec as {
                description?: string;
                model?: string;
                instructions: string;
                toolProfile?: string;
                tools: string[];
                knowledge: { collections?: string[] };
                notify: { destinations?: string[] };
                skills: { name: string; version?: string }[];
                egress: { allow: string[] };
            };
            const opt = (v: string | undefined) => (s === '' ? undefined : v);
            expect(doc.metadata.name).toBe(s);
            expect(sp.description).toBe(opt(s));
            expect(sp.model).toBe(s);
            expect(sp.instructions).toBe(s);
            expect(sp.toolProfile).toBe(opt(s));
            expect(sp.tools).toEqual([s, 'b']);
            expect(sp.knowledge.collections).toEqual([s]);
            expect(sp.notify.destinations).toEqual([s]);
            expect(sp.skills).toEqual([{ name: s, version: s }]);
            expect(sp.egress.allow).toEqual([s]);
        });
    }
    it('keeps the trailing spaces of a block scalar', () => {
        const out = renderSpecYaml('a', '', { model: 'm', instructions: 'keep   \nthese  \n' });
        expect(out).toContain('    keep   \n    these  \n');
    });
    it('quotes what needs quoting and nothing else', () => {
        expect(scalar('claude-haiku')).toBe('claude-haiku');
        expect(scalar('Ready')).toBe('Ready');
        expect(scalar('a: b')).toBe('"a: b"');
        expect(scalar('')).toBe('""');
        expect(scalar('<tag>&')).toBe('"<tag>&"');
    });
});
