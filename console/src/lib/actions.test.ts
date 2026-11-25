import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { trapFocus } from './actions';

// A page shaped like the console: a shell with the app, a live region for toasts, and an overlay with its scrim.
let app: HTMLElement;
let main: HTMLElement;
let toast: HTMLElement;
let opener: HTMLButtonElement;
let scrim: HTMLElement;
let dlg: HTMLElement;
let inside1: HTMLButtonElement;
let inside2: HTMLButtonElement;

function open() {
    return trapFocus(dlg, {});
}

beforeEach(() => {
    document.body.innerHTML = `
        <div id="app">
          <aside id="nav"><a href="/x">nav</a></aside>
          <main id="main"><button id="opener">open</button><button id="other">other</button></main>
          <div id="toast" data-inert-exempt role="status"><button>dismiss</button></div>
          <div id="scrim" data-overlay-part></div>
          <div id="dlg" role="dialog"><button id="in1">one</button><button id="in2">two</button></div>
        </div>`;
    app = document.getElementById('app')!;
    main = document.getElementById('main')!;
    toast = document.getElementById('toast')!;
    scrim = document.getElementById('scrim')!;
    dlg = document.getElementById('dlg')!;
    opener = document.getElementById('opener') as HTMLButtonElement;
    inside1 = document.getElementById('in1') as HTMLButtonElement;
    inside2 = document.getElementById('in2') as HTMLButtonElement;
    opener.focus();
});
afterEach(() => {
    document.body.innerHTML = '';
});

describe('trapFocus', () => {
    it('makes everything outside the overlay inert, except the scrim and the live region', () => {
        const t = open();
        expect(main).toHaveAttribute('inert');
        expect(document.getElementById('nav')).toHaveAttribute('inert');
        expect(scrim).not.toHaveAttribute('inert');
        expect(toast).not.toHaveAttribute('inert');
        expect(dlg).not.toHaveAttribute('inert');
        expect(app).not.toHaveAttribute('inert');
        t.destroy();
    });

    it('lifts the inert attributes it set, and only those, when the overlay closes', () => {
        document.getElementById('nav')!.setAttribute('inert', '');
        const t = open();
        t.destroy();
        expect(main).not.toHaveAttribute('inert');
        expect(document.getElementById('nav')).toHaveAttribute('inert'); // it was inert before, and stays so
    });

    it('returns focus to the opener on close', () => {
        const t = open();
        expect(dlg.contains(document.activeElement)).toBe(true);
        t.destroy();
        expect(document.activeElement).toBe(opener);
    });

    it('pulls focus back when something outside is focused', () => {
        const t = open();
        inside2.focus();
        document.getElementById('other')!.focus();
        expect(dlg.contains(document.activeElement)).toBe(true);
        expect(document.activeElement).toBe(inside2); // where the person was
        t.destroy();
    });

    it('recovers focus that a mouse click left on the page body', async () => {
        const t = open();
        inside2.focus();
        inside2.blur(); // a click on empty space: focus goes to the body
        await new Promise((r) => setTimeout(r, 5));
        expect(dlg.contains(document.activeElement)).toBe(true);
        t.destroy();
    });

    it('wraps Tab and Shift+Tab inside the overlay', () => {
        const t = open();
        inside2.focus();
        inside2.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
        expect(document.activeElement).toBe(inside1);
        inside1.dispatchEvent(
            new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }),
        );
        expect(document.activeElement).toBe(inside2);
        t.destroy();
    });

    it('closes on Escape', () => {
        let closed = 0;
        const t = trapFocus(dlg, { onescape: () => closed++ });
        inside1.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
        expect(closed).toBe(1);
        t.destroy();
    });

    it('stops listening after it is destroyed', () => {
        const t = open();
        t.destroy();
        document.getElementById('other')!.focus();
        expect(document.activeElement).toBe(document.getElementById('other'));
    });
});
