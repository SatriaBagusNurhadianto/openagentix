<script lang="ts">
    import type { Snippet } from 'svelte';
    import { trapFocus } from '$lib/actions';

    interface Props {
        title: Snippet;
        children: Snippet;
        actions: Snippet;
        onclose: () => void;
        destructive?: boolean;
        /** Selector of the element to focus first. Default: the first focusable one. */
        initial?: string;
    }
    let { title, children, actions, onclose, destructive = false, initial }: Props = $props();
    const uid = $props.id();
</script>

<div class="scrim" role="presentation" onclick={onclose}></div>
<div
    class="dialog"
    role={destructive ? 'alertdialog' : 'dialog'}
    aria-modal="true"
    aria-labelledby="{uid}-t"
    aria-describedby="{uid}-b"
    use:trapFocus={{ initial, onescape: onclose }}
>
    <h2 id="{uid}-t">{@render title()}</h2>
    <div id="{uid}-b" class="flex flex-col gap-3">{@render children()}</div>
    <div class="acts">{@render actions()}</div>
</div>
