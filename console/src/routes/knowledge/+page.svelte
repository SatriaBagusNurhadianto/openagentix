<script lang="ts">
    import PlannedPage from '$lib/components/PlannedPage.svelte';
    import { PLANNED } from '$lib/planned';
</script>

<PlannedPage content={PLANNED['knowledge']!} />
