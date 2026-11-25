<script lang="ts">
    interface Props {
        label: string;
        value: string;
        id?: string;
        hint?: string;
        error?: string;
        counter?: string;
        counterOver?: boolean;
        /** The counter is announced only when the draft is close to the limit (UI-SPEC section 5). */
        counterLive?: boolean;
        readonly?: boolean;
        rows?: number;
        oninput?: () => void;
        textarea?: HTMLTextAreaElement;
    }
    let {
        label,
        value = $bindable(),
        id,
        hint,
        error,
        counter,
        counterOver = false,
        counterLive = false,
        readonly = false,
        rows = 10,
        oninput,
        textarea = $bindable(),
    }: Props = $props();
    const uid = $props.id();
    const fid = $derived(id ?? `f-${uid}`);
    const described = $derived(
        [hint ? `${fid}-hint` : '', error ? `${fid}-err` : ''].filter(Boolean).join(' ') || undefined,
    );
</script>

<div class="field {error ? 'bad' : ''}">
    <label for={fid}>{label}</label>
    <textarea
        id={fid}
        bind:this={textarea}
        bind:value
        {rows}
        {readonly}
        spellcheck="true"
        aria-invalid={error ? 'true' : undefined}
        aria-describedby={described}
        {oninput}
    ></textarea>
    <div class="hint">
        <span id="{fid}-hint">{hint ?? ''}</span>
        {#if counter}<span class="num {counterOver ? 'text-refused' : ''}" aria-live={counterLive ? 'polite' : 'off'}
                >{counter}</span
            >{/if}
    </div>
    {#if error}
        <div class="err" id="{fid}-err">
            <svg class="i" aria-hidden="true"><use href="#s-refused" /></svg><span>{error}</span>
        </div>
    {/if}
</div>
