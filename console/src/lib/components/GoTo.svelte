<script lang="ts">
    // "Go to": the search box of the top bar. Search only navigates in this release (UI-SPEC section 9).
    import { goto } from '$app/navigation';
    import { api } from '$lib/api';
    import { NAV, SETTINGS } from '$lib/nav';
    import type { Agent } from '$lib/types';
    import Dialog from './Dialog.svelte';
    import Button from './Button.svelte';

    interface Props {
        onclose: () => void;
    }
    let { onclose }: Props = $props();
    let q = $state('');
    let agents = $state<Agent[]>([]);
    let active = $state(0);

    $effect(() => {
        const c = new AbortController();
        api.agents(c.signal)
            .then((l) => (agents = l.items))
            .catch(() => {});
        return () => c.abort();
    });

    const pages = [...NAV.flatMap((g) => g.items), SETTINGS].map((i) => ({
        label: i.label,
        href: i.href,
        kind: 'Page',
    }));
    const results = $derived.by(() => {
        const t = q.trim().toLowerCase();
        const all = [
            ...agents.map((a) => ({ label: a.name, href: `/agents/${a.name}`, kind: 'Agent' })),
            ...pages,
            { label: 'Playground', href: '/agents/playground', kind: 'Page' },
        ];
        return (t ? all.filter((r) => r.label.toLowerCase().includes(t)) : all).slice(0, 12);
    });
    $effect(() => {
        void results;
        active = 0;
    });

    function go(href: string) {
        onclose();
        void goto(href);
    }
    function key(e: KeyboardEvent) {
        if (e.key === 'ArrowDown') {
            e.preventDefault();
            active = Math.min(active + 1, results.length - 1);
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            active = Math.max(active - 1, 0);
        } else if (e.key === 'Enter') {
            const r = results[active];
            if (r) {
                e.preventDefault();
                go(r.href);
            }
        }
    }
</script>

<Dialog {onclose} initial="input">
    {#snippet title()}Go to{/snippet}
    <div class="goto">
        <input
            class="input"
            type="search"
            placeholder="Agent or page"
            aria-label="Go to an agent or a page"
            bind:value={q}
            onkeydown={key}
            autocomplete="off"
        />
        <ul aria-label="Results">
            {#each results as r, i (r.href + r.label)}
                <li>
                    <a href={r.href} data-active={i === active} onclick={(e) => (e.preventDefault(), go(r.href))}
                        ><span class={r.kind === 'Agent' ? 'mono' : ''}>{r.label}</span>
                        <span class="sm t3">{r.kind}</span></a
                    >
                </li>
            {:else}
                <li class="sm t3 p-2">Nothing matches.</li>
            {/each}
        </ul>
    </div>
    {#snippet actions()}<Button onclick={onclose}>Close</Button>{/snippet}
</Dialog>
