<script lang="ts">
    import type { Snippet } from 'svelte';
    import '../app.css';
    import AppShell from '$lib/components/AppShell.svelte';
    import IconSprite from '$lib/components/IconSprite.svelte';
    import SignIn from '$lib/components/SignIn.svelte';
    import ToastRegion from '$lib/components/ToastRegion.svelte';
    import { auth } from '$lib/auth.svelte';
    import { theme } from '$lib/theme.svelte';

    let { children }: { children: Snippet } = $props();

    $effect(() => theme.init());
</script>

<svelte:head><title>OpenAgentix console</title></svelte:head>

<div class="app-root">
    <IconSprite />
    {#if auth.signedIn}
        <AppShell>{@render children()}</AppShell>
    {:else}
        <SignIn />
    {/if}
    <ToastRegion />
</div>
