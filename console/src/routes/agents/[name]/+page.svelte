<script lang="ts">
    import { page } from '$app/state';
    import AgentBuilder from '$lib/components/AgentBuilder.svelte';
</script>

{#key page.params.name}
    <AgentBuilder name={page.params.name} />
{/key}
