<script lang="ts">
    // Read-only code or YAML with line numbers, a copy button and a wrap toggle. The <pre> is focusable so that it scrolls by keyboard.
    import { copyText } from '$lib/actions';
    import { toasts } from '$lib/toast.svelte';
    import { highlightYaml } from '$lib/yaml';
    import IconButton from './IconButton.svelte';

    interface Props {
        code: string;
        label: string;
        /** 1-based line numbers marked as changed (draft against cluster). */
        changed?: Set<number>;
        highlight?: boolean;
        copyLabel?: string;
    }
    let { code, label, changed = new Set<number>(), highlight = true, copyLabel }: Props = $props();
    let wrap = $state(false);
    const lines = $derived(code.replace(/\n$/, '').split('\n'));
    async function copy() {
        if (await copyText(code)) toasts.success('Copied');
        else toasts.error('Could not copy: the browser refused clipboard access. Select the text and copy it by hand.');
    }
</script>

<div class="code {wrap ? 'wrap' : ''}">
    <div class="copy flex gap-1">
        <IconButton icon="i-copy" label={copyLabel ?? `Copy ${label}`} onclick={copy} />
    </div>
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <pre tabindex="0" aria-label={label}><code
            >{#each lines as line, i (i)}<span class="ln {changed.has(i + 1) ? 'chg' : ''}"
                    >{#if highlight}{#each highlightYaml(line) as seg, j (j)}<span class={seg.cls}>{seg.text}</span
                            >{/each}{:else}{line}{/if}</span
                >{/each}</code
        ></pre>
</div>
<label class="sm t3 mt-1 flex items-center gap-2"
    ><input type="checkbox" bind:checked={wrap} class="accent-accent" /> Wrap long lines</label
>
