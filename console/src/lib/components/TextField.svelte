<script lang="ts">
    import type { Snippet } from 'svelte';
    // A label above a single-line control, with hint, counter and error below it (UI-SPEC section 5, "Field").
    interface Props {
        label: string;
        value: string;
        id?: string;
        hint?: string;
        error?: string;
        counter?: string;
        counterOver?: boolean;
        readonly?: boolean;
        disabled?: boolean;
        mono?: boolean;
        maxlength?: number;
        placeholder?: string;
        type?: 'text' | 'number' | 'search' | 'password';
        min?: number;
        max?: number;
        autocomplete?: 'off' | 'on';
        class?: string;
        oninput?: () => void;
        onchange?: () => void;
        input?: HTMLInputElement;
        /** A control shown after the input (the show and hide button of a secret). */
        trailing?: Snippet;
    }
    let {
        label,
        value = $bindable(),
        id,
        hint,
        error,
        counter,
        counterOver = false,
        readonly = false,
        disabled = false,
        mono = false,
        maxlength,
        placeholder,
        type = 'text',
        min,
        max,
        autocomplete = 'off',
        class: cls = '',
        oninput,
        onchange,
        input = $bindable(),
        trailing,
    }: Props = $props();
    const uid = $props.id();
    const fid = $derived(id ?? `f-${uid}`);
    const described = $derived(
        [hint ? `${fid}-hint` : '', error ? `${fid}-err` : ''].filter(Boolean).join(' ') || undefined,
    );
</script>

<div class="field {error ? 'bad' : ''} {cls}">
    <label for={fid}>{label}</label>
    <div class={trailing ? 'flex gap-2' : 'contents'}>
        <input
            id={fid}
            bind:this={input}
            bind:value
            class={mono ? 'mono' : ''}
            {type}
            {readonly}
            {disabled}
            {maxlength}
            {placeholder}
            {min}
            {max}
            {autocomplete}
            spellcheck="false"
            aria-invalid={error ? 'true' : undefined}
            aria-describedby={described}
            {oninput}
            {onchange}
        />
        {@render trailing?.()}
    </div>
    {#if hint || counter}
        <div class="hint">
            <span id="{fid}-hint">{hint ?? ''}</span>
            {#if counter}<span class="num {counterOver ? 'text-refused' : ''}">{counter}</span>{/if}
        </div>
    {/if}
    {#if error}
        <div class="err" id="{fid}-err">
            <svg class="i" aria-hidden="true"><use href="#s-refused" /></svg><span>{error}</span>
        </div>
    {/if}
</div>
