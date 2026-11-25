<script lang="ts">
    import type { Snippet } from 'svelte';
    import Icon from './Icon.svelte';

    interface Props {
        kind?: 'info' | 'ro' | 'refused' | 'error' | 'warn';
        title?: string;
        /** After an action: announce at once. Otherwise a polite status. */
        alert?: boolean;
        children?: Snippet;
        action?: Snippet;
        headingId?: string;
        headingLevel?: 2 | 3;
    }
    let { kind = 'info', title, alert = false, children, action, headingId, headingLevel = 2 }: Props = $props();
    const icon = $derived(
        { info: 'i-info', ro: 'i-lock', refused: 's-refused', error: 's-error', warn: 's-pending' }[kind],
    );
    let heading: HTMLElement | undefined = $state();
    /** Moves focus to the title, used after a refused save. */
    export function focus(): void {
        heading?.focus();
    }
</script>

<div class="notice {kind === 'error' ? 'refused' : kind}" role={alert || kind === 'error' ? 'alert' : 'status'}>
    <Icon name={icon} />
    <div class="min-w-0 grow">
        {#if title}
            <svelte:element this={`h${headingLevel}`} class="nt" id={headingId} tabindex="-1" bind:this={heading}
                >{title}</svelte:element
            >
        {/if}
        {@render children?.()}
    </div>
    {#if action}<div class="shrink-0">{@render action()}</div>{/if}
</div>
