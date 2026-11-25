<script lang="ts">
    import type { Condition } from '$lib/types';
    import { conditionViews, railName } from '$lib/status';

    interface Props {
        conditions: Condition[];
        compact?: boolean;
        class?: string;
    }
    let { conditions, compact = false, class: cls = '' }: Props = $props();
    const views = $derived(conditionViews(conditions));
    const icon: Record<string, string> = {
        ready: 's-ready',
        pending: 's-pending',
        refused: 's-refused',
        error: 's-error',
        none: 's-none',
    };
</script>

{#if compact}
    <span
        class="pips {cls}"
        role="img"
        aria-label={views.map(railName).join(', ')}
        title={views.map(railName).join('\n')}
    >
        {#each views as v (v.type)}<i class="pip {v.kind}"></i>{/each}
    </span>
{:else}
    <div class="rail {cls}" role="list" aria-label="Conditions">
        {#each views as v (v.type)}
            <span role="listitem" class={v.kind === 'none' ? 'rc none' : `rc st-${v.kind}`} title={railName(v)}>
                <svg aria-hidden="true" focusable="false"><use href="#{icon[v.kind]}" /></svg>{v.type}
                <span class="sr">{railName(v).slice(v.type.length)}</span>
            </span>
        {/each}
    </div>
{/if}
