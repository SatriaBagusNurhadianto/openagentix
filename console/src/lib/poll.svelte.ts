// Polling that stops while the tab is hidden (UI-SPEC: a status that is not settled is refreshed every 5 s).

export interface PollOptions {
    /** Called on every tick. */
    run: () => Promise<void> | void;
    /** Keep polling while this is true. Checked before every tick. */
    active: () => boolean;
    intervalMs?: number;
}

export interface Poller {
    /** Look at `active()` again and arm the timer if needed (call after something changed). */
    kick(): void;
    stop(): void;
}

export function startPolling(o: PollOptions): Poller {
    const every = o.intervalMs ?? 5000;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;
    const visible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';
    function arm() {
        clearTimeout(timer);
        timer = undefined;
        if (stopped || !visible() || !o.active()) return;
        timer = setTimeout(async () => {
            timer = undefined;
            if (stopped) return;
            if (visible() && o.active()) {
                try {
                    await o.run();
                } catch {
                    /* the owner shows its own error; polling goes on */
                }
            }
            arm();
        }, every);
    }
    const onVisibility = () => (visible() ? arm() : (clearTimeout(timer), (timer = undefined)));
    document.addEventListener('visibilitychange', onVisibility);
    arm();
    return {
        kick: () => {
            if (timer === undefined) arm();
        },
        stop: () => {
            stopped = true;
            clearTimeout(timer);
            document.removeEventListener('visibilitychange', onVisibility);
        },
    };
}
