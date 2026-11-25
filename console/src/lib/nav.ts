export interface NavItem {
    id: string;
    label: string;
    href: string;
    icon: string;
    locked?: boolean;
    sub?: { label: string; href: string }[];
}
export interface NavGroup {
    label: string | null;
    items: NavItem[];
}

/** The navigation of UI-SPEC section 4.2, in order. The lock marks a page that is read-only (or not available) in this release. */
export const NAV: NavGroup[] = [
    { label: null, items: [{ id: 'overview', label: 'Overview', href: '/', icon: 'i-overview' }] },
    {
        label: 'Build',
        items: [
            {
                id: 'agents',
                label: 'Agents',
                href: '/agents',
                icon: 'i-agent',
                sub: [
                    { label: 'Registry', href: '/agents' },
                    { label: 'Playground', href: '/agents/playground' },
                ],
            },
            { id: 'skills', label: 'Skills', href: '/skills', icon: 'i-skill', locked: true },
        ],
    },
    {
        label: 'Connect',
        items: [
            { id: 'models', label: 'Models', href: '/models', icon: 'i-model', locked: true },
            { id: 'tools', label: 'Tools', href: '/tools', icon: 'i-tool', locked: true },
            { id: 'knowledge', label: 'Knowledge', href: '/knowledge', icon: 'i-book', locked: true },
            { id: 'channels', label: 'Channels and triggers', href: '/channels', icon: 'i-channel', locked: true },
            { id: 'schedules', label: 'Schedules', href: '/schedules', icon: 'i-clock', locked: true },
        ],
    },
    {
        label: 'Operate',
        items: [
            { id: 'traces', label: 'Traces', href: '/traces', icon: 'i-trace' },
            { id: 'costs', label: 'Costs', href: '/costs', icon: 'i-coin' },
            { id: 'memory', label: 'Memory', href: '/memory', icon: 'i-memory', locked: true },
        ],
    },
    {
        label: 'Govern',
        items: [{ id: 'governance', label: 'Governance', href: '/governance', icon: 'i-shield', locked: true }],
    },
];
export const SETTINGS: NavItem = { id: 'settings', label: 'Settings', href: '/settings', icon: 'i-gear' };

/** The navigation item a path belongs to ("/agents/researcher" keeps Agents active). */
export function activeId(path: string): string {
    const p = path.replace(/\/+$/, '') || '/';
    if (p === '/') return 'overview';
    const seg = p.split('/')[1] ?? '';
    return [...NAV.flatMap((g) => g.items), SETTINGS].find((i) => i.id === seg)?.id ?? '';
}

export function breadcrumb(path: string): string[] {
    const p = path.replace(/\/+$/, '') || '/';
    if (p === '/') return ['Overview'];
    const parts = p.split('/').filter(Boolean);
    const item = [...NAV.flatMap((g) => g.items), SETTINGS].find((i) => i.id === parts[0]);
    const out = [item?.label ?? parts[0] ?? ''];
    if (parts[0] === 'agents' && parts[1])
        out.push(parts[1] === 'playground' ? 'Playground' : parts[1] === 'new' ? 'New agent' : (parts[1] ?? ''));
    return out;
}

/** `g` then a letter (UI-SPEC section 8). */
export const GO_KEYS: Record<string, string> = {
    o: '/',
    a: '/agents',
    p: '/agents/playground',
    t: '/tools',
    k: '/knowledge',
    m: '/models',
    c: '/costs',
    r: '/traces',
    v: '/governance',
};

export function isTypingTarget(t: EventTarget | null): boolean {
    if (!(t instanceof HTMLElement)) return false;
    return t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName);
}
