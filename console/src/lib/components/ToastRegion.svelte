<script lang="ts">
    import { toasts } from '$lib/toast.svelte';
    import Icon from './Icon.svelte';
    import IconButton from './IconButton.svelte';
</script>

<div class="toasts" data-inert-exempt="" role="status" aria-live="polite">
    {#each toasts.items as t (t.id)}
        <div
            class="toast {t.kind === 'error' ? 'err' : t.kind === 'info' ? 'info' : ''}"
            role={t.kind === 'error' ? 'alert' : undefined}
            onmouseenter={() => toasts.pause(t.id)}
            onmouseleave={() => toasts.resume(t.id)}
            onfocusin={() => toasts.pause(t.id)}
            onfocusout={() => toasts.resume(t.id)}
        >
            <Icon name={t.kind === 'error' ? 's-error' : t.kind === 'info' ? 'i-info' : 's-ready'} />
            <span>{t.text}</span>
            <IconButton icon="i-x" label="Dismiss" iconSize={12} onclick={() => toasts.dismiss(t.id)} />
        </div>
    {/each}
</div>
