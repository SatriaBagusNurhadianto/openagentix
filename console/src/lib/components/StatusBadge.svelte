<script lang="ts">
    import type { State } from '$lib/types';
    import { stateWord } from '$lib/status';

    interface Props {
        state: State;
        deleting?: boolean;
        title?: string;
        word?: string;
    }
    let { state, deleting = false, title, word }: Props = $props();
    const icon: Record<State, string> = {
        ready: 's-ready',
        pending: 's-pending',
        refused: 's-refused',
        error: 's-error',
        paused: 's-paused',
    };
    const label = $derived(word ?? stateWord(state, deleting));
</script>

<span class="badge st-{state}" {title}
    ><svg aria-hidden="true" focusable="false"><use href="#{icon[state]}" /></svg>{label}</span
>
