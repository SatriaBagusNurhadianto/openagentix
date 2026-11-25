export type ToastKind = 'success' | 'info' | 'error';
export interface ToastItem {
    id: number;
    kind: ToastKind;
    text: string;
}

const MAX = 3;
let items = $state<ToastItem[]>([]);
let next = 1;
// timers are plain bookkeeping, not rendered state
// eslint-disable-next-line svelte/prefer-svelte-reactivity
const timers = new Map<number, ReturnType<typeof setTimeout>>();

function arm(id: number, ms = 6000): void {
    clearTimeout(timers.get(id));
    timers.set(
        id,
        setTimeout(() => toasts.dismiss(id), ms),
    );
}

export const toasts = {
    get items(): ToastItem[] {
        return items;
    },
    push(kind: ToastKind, text: string): number {
        const id = next++;
        items = [...items, { id, kind, text }].slice(-MAX);
        // an error stays until it is dismissed (UI-SPEC section 5)
        if (kind !== 'error') arm(id);
        return id;
    },
    success: (text: string) => toasts.push('success', text),
    info: (text: string) => toasts.push('info', text),
    error: (text: string) => toasts.push('error', text),
    dismiss(id: number): void {
        clearTimeout(timers.get(id));
        timers.delete(id);
        items = items.filter((t) => t.id !== id);
    },
    pause(id: number): void {
        clearTimeout(timers.get(id));
    },
    resume(id: number): void {
        const t = items.find((x) => x.id === id);
        if (t && t.kind !== 'error') arm(id);
    },
    clear(): void {
        for (const t of timers.values()) clearTimeout(t);
        timers.clear();
        items = [];
    },
};
