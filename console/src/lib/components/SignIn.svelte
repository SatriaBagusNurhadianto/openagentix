<script lang="ts">
    import { auth } from '$lib/auth.svelte';
    import { ctx } from '$lib/context.svelte';
    import Button from './Button.svelte';
    import Notice from './Notice.svelte';
    import TextField from './TextField.svelte';

    let token = $state('');
    let busy = $state(false);
    let show = $state(false);
    let local = $state('');
    const error = $derived(local || ctx.signInError);
    let input: HTMLInputElement | undefined = $state();

    async function submit(e: SubmitEvent) {
        e.preventDefault();
        if (!token.trim()) {
            local = 'Enter the console token.';
            input?.focus();
            return;
        }
        busy = true;
        local = '';
        ctx.setSignInError('');
        auth.signIn(token);
        const ok = await ctx.load();
        busy = false;
        if (!ok) {
            const reason = ctx.error ?? '';
            auth.signOut();
            ctx.setSignInError(
                reason.startsWith('The console token')
                    ? reason
                    : `Could not sign in: ${reason || 'the console API did not answer.'}`,
            );
            token = '';
            input?.focus();
        }
    }
</script>

<main class="signin" id="content">
    <div class="panel">
        <div class="hd">
            <div class="mark" aria-hidden="true">
                <svg viewBox="0 0 14 14"
                    ><circle cx="3" cy="7" r="1.8" /><circle cx="11" cy="7" r="1.8" /><path
                        d="M4.8 7h4.4M7 4.5v5"
                    /></svg
                >
            </div>
            <div class="word"><span>Open</span><b>Agentix</b></div>
        </div>
        <form class="bd" onsubmit={submit}>
            <h1>Sign in</h1>
            <p class="t2">
                The console acts as one operator. Paste the console token. It is kept in this tab's memory only, and a
                reload asks again.
            </p>
            {#if error}
                <Notice kind="error" alert><p class="sm">{error}</p></Notice>
            {/if}
            <TextField
                label="Console token"
                bind:value={token}
                bind:input
                type={show ? 'text' : 'password'}
                mono
                autocomplete="off"
                hint="From the Secret console-token in the agents namespace."
            >
                {#snippet trailing()}
                    <Button
                        type="button"
                        variant="secondary"
                        aria-pressed={show ? 'true' : 'false'}
                        onclick={() => (show = !show)}>{show ? 'Hide token' : 'Show token'}</Button
                    >
                {/snippet}
            </TextField>
            <div><Button variant="primary" type="submit" {busy}>Sign in</Button></div>
        </form>
    </div>
</main>
