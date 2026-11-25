<script lang="ts">
    import type { Snippet } from 'svelte';

    interface Props {
        title: string;
        /** The resource strip: the kind and group the page reads or writes, or its upstream source. */
        strip?: string;
        mono?: boolean;
        badge?: Snippet;
        actions?: Snippet;
    }
    let { title, strip, mono = false, badge, actions }: Props = $props();
</script>

<div class="ph">
    <div class="ph-l">
        <div class="ph-t">
            <h1 class={mono ? 'mono-title' : ''}>{title}</h1>
            {@render badge?.()}
        </div>
        {#if strip}<div class="strip">{strip}</div>{/if}
    </div>
    {#if actions}<div class="acts">{@render actions()}</div>{/if}
</div>
