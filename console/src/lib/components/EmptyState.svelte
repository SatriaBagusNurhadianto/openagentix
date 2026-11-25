<script lang="ts">
    import type { Snippet } from 'svelte';
    import Icon from './Icon.svelte';

    interface Props {
        icon?: string;
        title: string;
        children?: Snippet;
        action?: Snippet;
        level?: 2 | 3;
    }
    let { icon = 'i-agent', title, children, action, level = 2 }: Props = $props();
</script>

<div class="empty">
    <span class="ico"><Icon name={icon} size={20} /></span>
    <svelte:element this={`h${level}`} class="font-semibold text-md">{title}</svelte:element>
    <div class="max-w-[52ch] text-text-2">{@render children?.()}</div>
    {#if action}<div class="mt-1">{@render action()}</div>{/if}
</div>
