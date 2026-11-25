<script lang="ts">
    import type { Snippet } from 'svelte';
    import type { HTMLButtonAttributes } from 'svelte/elements';

    interface Props extends Omit<HTMLButtonAttributes, 'class'> {
        variant?: 'secondary' | 'primary' | 'ghost' | 'danger';
        small?: boolean;
        busy?: boolean;
        /** Why the button does nothing right now. It stays focusable and the reason is read with it. */
        disabledReason?: string;
        class?: string;
        children: Snippet;
    }
    let {
        variant = 'secondary',
        small = false,
        busy = false,
        disabledReason,
        class: cls = '',
        children,
        onclick,
        id,
        ...rest
    }: Props = $props();
    const uid = $props.id();
    const blocked = $derived(busy || !!disabledReason || rest.disabled === true);
    function click(e: MouseEvent & { currentTarget: EventTarget & HTMLButtonElement }) {
        if (blocked) {
            e.preventDefault();
            return;
        }
        (onclick as ((e: MouseEvent) => void) | undefined)?.(e);
    }
</script>

<button
    {...rest}
    {id}
    disabled={undefined}
    class="btn {variant === 'primary'
        ? 'pri'
        : variant === 'ghost'
          ? 'ghost'
          : variant === 'danger'
            ? 'danger'
            : ''} {small ? 'sm' : ''} {busy ? 'busy' : ''} {cls}"
    aria-disabled={blocked ? 'true' : undefined}
    aria-busy={busy ? 'true' : undefined}
    aria-describedby={disabledReason ? `${uid}-why` : rest['aria-describedby']}
    onclick={click}
>
    {#if busy}<span class="spin" aria-hidden="true"></span>{/if}
    {@render children()}
</button>
{#if disabledReason}<span id="{uid}-why" class="sr">{disabledReason}</span>{/if}
