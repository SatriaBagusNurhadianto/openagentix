import type { Spec } from './types';

// Colouring of one YAML line for the code viewer: keys, strings and comments, with text tokens and the accent only.
export interface Seg {
    text: string;
    cls: string;
}

export function highlightYaml(line: string): Seg[] {
    if (/^\s*#/.test(line)) return [{ text: line, cls: 'c' }];
    const m = /^(\s*(?:- )?)([A-Za-z0-9_./-]+:)(\s+.*)?$/.exec(line);
    if (m) {
        const out: Seg[] = [];
        if (m[1]) out.push({ text: m[1], cls: '' });
        out.push({ text: m[2] ?? '', cls: 'k' });
        const rest = m[3];
        if (rest)
            out.push({ text: rest, cls: /^\s+["'][^]*["']$|^\s+[A-Za-z][A-Za-z0-9./:_-]*$/.test(rest) ? 's' : 'v' });
        return out;
    }
    return [{ text: line, cls: 'v' }];
}

// --- a client-side rendering of the spec, for the "Draft" view of the YAML sheet ---

const PLAIN = /^[A-Za-z_][A-Za-z0-9_./@-]*$/;
const RESERVED = new Set(['true', 'false', 'null', 'yes', 'no', 'on', 'off', '~']);

export function scalar(s: string): string {
    if (s === '') return '""';
    if (PLAIN.test(s) && !RESERVED.has(s.toLowerCase())) return s;
    return JSON.stringify(s);
}

function list(out: string[], indent: number, key: string, items: string[] | undefined): void {
    if (!items?.length) return;
    out.push(`${' '.repeat(indent)}${key}:`);
    for (const i of items) out.push(`${' '.repeat(indent + 2)}- ${scalar(i)}`);
}

/** The metadata and spec of a definition as YAML, in the order the console's server writes them. */
export function renderSpecYaml(name: string, namespace: string, s: Spec): string {
    const o: string[] = [
        'apiVersion: openagentix.io/v1alpha1',
        'kind: AgentDefinition',
        'metadata:',
        `  name: ${scalar(name)}`,
    ];
    if (namespace) o.push(`  namespace: ${scalar(namespace)}`);
    o.push('spec:');
    if (s.description) o.push(`  description: ${scalar(s.description)}`);
    o.push(`  model: ${scalar(s.model)}`);
    const text = s.instructions;
    if (!text.includes('\n') && !text.startsWith(' ')) o.push(`  instructions: ${scalar(text)}`);
    else {
        const mark = !text.endsWith('\n') ? '|-' : text.endsWith('\n\n') ? '|+' : '|';
        o.push(`  instructions: ${text.startsWith(' ') || text.startsWith('\n') ? mark + '2' : mark}`);
        for (const l of text.replace(/\n$/, '').split('\n')) o.push(l === '' ? '' : `    ${l}`);
    }
    if (s.toolProfile) o.push(`  toolProfile: ${scalar(s.toolProfile)}`);
    list(o, 2, 'tools', s.tools);
    for (const [k, f, key] of [
        ['knowledge', s.knowledge, 'collections'],
        ['notify', s.notify, 'destinations'],
    ] as const) {
        if (!f) continue;
        o.push(`  ${k}:`, `    enabled: ${f.enabled}`);
        list(o, 4, key, key === 'collections' ? f.collections : f.destinations);
    }
    if (s.memory) {
        o.push('  memory:', `    enabled: ${s.memory.enabled}`);
        if (s.memory.ttlDays) o.push(`    ttlDays: ${s.memory.ttlDays}`);
    }
    if (s.skills?.length) {
        o.push('  skills:');
        for (const k of s.skills) {
            o.push(`    - name: ${scalar(k.name)}`);
            if (k.version) o.push(`      version: ${scalar(k.version)}`);
        }
    }
    if (s.egress?.allow?.length) {
        o.push('  egress:');
        list(o, 4, 'allow', s.egress.allow);
    }
    if (s.suspend) o.push('  suspend: true');
    return o.join('\n') + '\n';
}

/** 1-based numbers of the lines of `draft` that do not appear (as many times) in `base`. */
export function changedLines(base: string, draft: string): Set<number> {
    const have = new Map<string, number>();
    for (const l of base.split('\n')) have.set(l, (have.get(l) ?? 0) + 1);
    const out = new Set<number>();
    draft
        .replace(/\n$/, '')
        .split('\n')
        .forEach((l, i) => {
            const n = have.get(l) ?? 0;
            if (n > 0) have.set(l, n - 1);
            else out.add(i + 1);
        });
    return out;
}
