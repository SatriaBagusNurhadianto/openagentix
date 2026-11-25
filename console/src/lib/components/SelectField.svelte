<script lang="ts">
    export interface Option {
        value: string;
        label: string;
        disabled?: boolean;
        /** Why the option cannot be chosen. It is shown after the label, in the option text itself. */
        reason?: string;
    }
    interface Props {
        label: string;
        value: string;
        options: Option[];
        id?: string;
        hint?: string;
        error?: string;
        disabled?: boolean;
        onchange?: () => void;
    }
    let { label, value = $bindable(), options, id, hint, error, disabled = false, onchange }: Props = $props();
    const uid = $props.id();
    const fid = $derived(id ?? `f-${uid}`);
    const described = $derived(
        [hint ? `${fid}-hint` : '', error ? `${fid}-err` : ''].filter(Boolean).join(' ') || undefined,
    );
</script>

<div class="field {error ? 'bad' : ''}">
    <label for={fid}>{label}</label>
    <select
        id={fid}
        bind:value
        {disabled}
        aria-invalid={error ? 'true' : undefined}
        aria-describedby={described}
        {onchange}
    >
        {#each options as o (o.value)}
            <option value={o.value} disabled={o.disabled}>{o.label}{o.reason ? ` — ${o.reason}` : ''}</option>
        {/each}
    </select>
    {#if hint}<div class="hint"><span id="{fid}-hint">{hint}</span></div>{/if}
    {#if error}
        <div class="err" id="{fid}-err">
            <svg class="i" aria-hidden="true"><use href="#s-refused" /></svg><span>{error}</span>
        </div>
    {/if}
</div>
