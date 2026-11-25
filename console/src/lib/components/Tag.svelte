<script lang="ts">
    import type { Snippet } from 'svelte';
    import Icon from './Icon.svelte';

    interface Props {
        variant?: 'neutral' | 'accent' | 'refused';
        mono?: boolean;
        onremove?: () => void;
        removeLabel?: string;
        title?: string;
        children: Snippet;
    }
    let { variant = 'neutral', onremove, removeLabel, title, children }: Props = $props();
</script>

<span class="tag {variant === 'accent' ? 'acc' : variant === 'refused' ? 'ref' : ''}" {title}>
    {@render children()}
    {#if onremove}
        <button type="button" aria-label={removeLabel ?? 'Remove'} onclick={onremove}
            ><Icon name="i-x" size={12} /></button
        >
    {/if}
</span>
