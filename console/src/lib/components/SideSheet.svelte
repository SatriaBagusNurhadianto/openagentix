<script lang="ts">
    import type { Snippet } from 'svelte';
    import { trapFocus } from '$lib/actions';
    import IconButton from './IconButton.svelte';

    interface Props {
        title: string;
        children: Snippet;
        headerExtra?: Snippet;
        onclose: () => void;
    }
    let { title, children, headerExtra, onclose }: Props = $props();
    const uid = $props.id();
</script>

<div class="scrim" role="presentation" onclick={onclose}></div>
<div
    class="sheet"
    role="dialog"
    aria-modal="true"
    aria-labelledby="{uid}-t"
    use:trapFocus={{ initial: '[data-close]', onescape: onclose }}
>
    <div class="hd">
        <h2 id="{uid}-t" class="mono !text-md">{title}</h2>
        <div class="ml-auto flex items-center gap-2">
            {@render headerExtra?.()}
            <IconButton icon="i-x" label="Close" onclick={onclose} data-close="" />
        </div>
    </div>
    <div class="bd">{@render children()}</div>
</div>
