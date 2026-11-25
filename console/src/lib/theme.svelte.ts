// Theme: system until the person chooses. The choice is the one thing besides the sidebar state kept in localStorage.

export type ThemeChoice = 'system' | 'light' | 'dark';

function read(key: string): string | null {
    try {
        return localStorage.getItem(key);
    } catch {
        return null;
    }
}
function write(key: string, v: string): void {
    try {
        localStorage.setItem(key, v);
    } catch {
        /* blocked storage: the choice lasts until the tab closes */
    }
}

function systemDark(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: dark)').matches;
}

let choice = $state<ThemeChoice>('system');

function apply(): void {
    const dark = choice === 'dark' || (choice === 'system' && systemDark());
    document.documentElement.classList.toggle('dark', dark);
}

export const theme = {
    get choice(): ThemeChoice {
        return choice;
    },
    get dark(): boolean {
        return choice === 'dark' || (choice === 'system' && systemDark());
    },
    init(): () => void {
        const v = read('oa-theme');
        choice = v === 'light' || v === 'dark' ? v : 'system';
        apply();
        if (typeof matchMedia !== 'function') return () => {};
        const mq = matchMedia('(prefers-color-scheme: dark)');
        const on = () => apply();
        mq.addEventListener?.('change', on);
        return () => mq.removeEventListener?.('change', on);
    },
    set(c: ThemeChoice): void {
        choice = c;
        if (c === 'system') {
            try {
                localStorage.removeItem('oa-theme');
            } catch {
                /* ignore */
            }
        } else write('oa-theme', c);
        apply();
    },
    toggle(): void {
        theme.set(theme.dark ? 'light' : 'dark');
    },
};

export const sidebarPref = {
    get collapsed(): boolean {
        return read('oa-sidebar') === 'collapsed';
    },
    set(collapsed: boolean): void {
        write('oa-sidebar', collapsed ? 'collapsed' : 'expanded');
    },
};

export const shortcutPref = {
    get on(): boolean {
        return read('oa-shortcuts') !== 'off';
    },
    set(on: boolean): void {
        write('oa-shortcuts', on ? 'on' : 'off');
    },
};
