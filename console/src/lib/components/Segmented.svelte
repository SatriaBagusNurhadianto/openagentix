<script lang="ts">
    // A pill group for filters: a radio group (arrow keys move and select).
    export interface Seg {
        id: string;
        label: string;
    }
    interface Props {
        options: Seg[];
        value: string;
        label: string;
        onselect: (id: string) => void;
    }
    let { options, value, label, onselect }: Props = $props();
    let root: HTMLElement | undefined = $state();
    function key(e: KeyboardEvent) {
        const i = options.findIndex((o) => o.id === value);
        let n = i;
        if (e.key === 'ArrowRight' || e.key === 'ArrowDown') n = (i + 1) % options.length;
        else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') n = (i - 1 + options.length) % options.length;
        else return;
        e.preventDefault();
        const o = options[n];
        if (!o) return;
        onselect(o.id);
        queueMicrotask(() => root?.querySelectorAll<HTMLElement>('[role="radio"]')[n]?.focus());
    }
</script>

<div class="seg" role="radiogroup" aria-label={label} bind:this={root} onkeydown={key} tabindex="-1">
    {#each options as o (o.id)}
        <button
            type="button"
            role="radio"
            aria-checked={value === o.id}
            tabindex={value === o.id ? 0 : -1}
            onclick={() => onselect(o.id)}>{o.label}</button
        >
    {/each}
</div>
