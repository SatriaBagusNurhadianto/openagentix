<script lang="ts">
    import type { Snippet } from 'svelte';
    interface Props {
        checked: boolean;
        children: Snippet;
        disabled?: boolean;
        /** Shown after the label as text (never only a grey look). */
        reason?: string;
        bad?: boolean;
        onchange?: (v: boolean) => void;
        id?: string;
    }
    let { checked = $bindable(), children, disabled = false, reason, bad = false, onchange, id }: Props = $props();
    const uid = $props.id();
</script>

<label class="chk {bad ? 'bad' : ''} {disabled ? 'dis' : ''}">
    <input
        {id}
        type="checkbox"
        bind:checked
        {disabled}
        aria-describedby={reason ? `${uid}-why` : undefined}
        onchange={() => onchange?.(checked)}
    />
    <span>
        {@render children()}
        {#if reason}<span class="why" id="{uid}-why">{reason}</span>{/if}
    </span>
</label>
