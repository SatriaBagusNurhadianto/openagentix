// Small DOM actions shared by the dialog, the side sheet and the mobile drawer.

const FOCUSABLE =
    'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function focusables(root: HTMLElement): HTMLElement[] {
    return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => !el.hasAttribute('hidden') && el.getAttribute('aria-hidden') !== 'true',
    );
}

export interface TrapOptions {
    /** A selector inside the node, or an element, to focus first. Default: the first focusable element. */
    initial?: string | HTMLElement;
    onescape?: () => void;
}

/**
 * Keeps keyboard focus inside the node while it exists, closes on Escape and gives focus back to the element that had it
 * before. This is the focus handling of every overlay (dialog, side sheet, drawer).
 */
export function trapFocus(node: HTMLElement, opts: TrapOptions = {}) {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    let current = opts;
    function first(): HTMLElement | null {
        const i = current.initial;
        if (typeof i === 'string') return node.querySelector<HTMLElement>(i);
        if (i) return i;
        return focusables(node)[0] ?? null;
    }
    (first() ?? node).focus();
    function onkey(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            e.stopPropagation();
            e.preventDefault();
            current.onescape?.();
            return;
        }
        if (e.key !== 'Tab') return;
        const els = focusables(node);
        if (els.length === 0) {
            e.preventDefault();
            return;
        }
        const a = els[0]!;
        const z = els[els.length - 1]!;
        const active = document.activeElement;
        if (e.shiftKey && (active === a || !node.contains(active))) {
            e.preventDefault();
            z.focus();
        } else if (!e.shiftKey && (active === z || !node.contains(active))) {
            e.preventDefault();
            a.focus();
        }
    }
    node.addEventListener('keydown', onkey);
    return {
        update(o: TrapOptions) {
            current = o;
        },
        destroy() {
            node.removeEventListener('keydown', onkey);
            if (opener && document.contains(opener)) opener.focus();
        },
    };
}

export async function copyText(text: string): Promise<boolean> {
    try {
        await navigator.clipboard.writeText(text);
        return true;
    } catch {
        // the clipboard API needs a secure context; localhost counts, a plain http address does not
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.setAttribute('readonly', '');
        ta.className = 'sr';
        document.body.appendChild(ta);
        ta.select();
        let ok = false;
        try {
            ok = document.execCommand('copy');
        } catch {
            ok = false;
        }
        ta.remove();
        return ok;
    }
}
