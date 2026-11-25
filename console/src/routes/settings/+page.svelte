<script lang="ts">
    import { goto } from '$app/navigation';
    import { auth } from '$lib/auth.svelte';
    import Button from '$lib/components/Button.svelte';
    import PageHeader from '$lib/components/PageHeader.svelte';
    import Segmented from '$lib/components/Segmented.svelte';
    import Switch from '$lib/components/Switch.svelte';
    import { ctx } from '$lib/context.svelte';
    import { shortcutPref, theme, type ThemeChoice } from '$lib/theme.svelte';
    import { toasts } from '$lib/toast.svelte';

    let shortcuts = $state(shortcutPref.on);
    const s = $derived(ctx.session);
    const p = $derived(ctx.platform);

    function signOut() {
        auth.signOut();
        ctx.reset();
        toasts.clear();
        void goto('/');
    }
</script>

<svelte:head><title>Settings · OpenAgentix console</title></svelte:head>

<section class="view max-form">
    <PageHeader title="Settings" strip="console · platform catalog" />
    <div class="grid g2">
        <div class="panel">
            <div class="hd"><h2>Appearance</h2></div>
            <div class="bd stack">
                <div class="field">
                    <span class="flabel" id="theme-l">Theme</span>
                    <Segmented
                        label="Theme"
                        value={theme.choice}
                        options={[
                            { id: 'system', label: 'System' },
                            { id: 'light', label: 'Light' },
                            { id: 'dark', label: 'Dark' },
                        ]}
                        onselect={(id) => theme.set(id as ThemeChoice)}
                    />
                </div>
                <div class="cap pt-3">
                    <div class="cx">
                        <span class="flabel" id="lks">Single-key shortcuts</span>
                        <span class="sm t3"
                            ><kbd>g</kbd> then a letter to move between pages, <kbd>/</kbd> to search, <kbd>[</kbd> to collapse
                            the sidebar.</span
                        >
                    </div>
                    <Switch labelledby="lks" bind:checked={shortcuts} onchange={(v) => shortcutPref.set(v)} />
                </div>
            </div>
        </div>
        <div class="panel">
            <div class="hd"><h2>Session</h2></div>
            <div class="bd stack">
                <dl class="kv sm">
                    <dt>Signed in with</dt>
                    <dd>console token · held in memory for this tab</dd>
                    <dt>Acts as</dt>
                    <dd>
                        <code>{s?.subject ?? 'console'}</code> · {s?.role ?? 'operator'}{s && !s.canWrite
                            ? ' (read-only: this console refuses every change)'
                            : ''}
                    </dd>
                    <dt>Namespace</dt>
                    <dd><code>{s?.namespace ?? ''}</code></dd>
                </dl>
                <p class="sm t3">The token is rotated by the installer. Signing out clears it from this tab.</p>
                <div><Button onclick={signOut}>Sign out</Button></div>
            </div>
        </div>
        <div class="panel">
            <div class="hd"><h2>Adapters</h2></div>
            <div class="bd">
                {#if p}
                    <dl class="kv sm">
                        <dt>Runtime</dt>
                        <dd><code>{p.adapters.runtime}</code></dd>
                        <dt>Model gateway</dt>
                        <dd><code>{p.adapters.modelGateway}</code></dd>
                        <dt>Tools</dt>
                        <dd><code>{p.adapters.tools}</code></dd>
                        <dt>Gateway URL</dt>
                        <dd><code>{p.gatewayBaseUrl}</code></dd>
                        <dt>A2A gate</dt>
                        <dd>{p.gateEnabled ? 'on' : 'off'}</dd>
                    </dl>
                {:else}
                    <p class="sm t3">{ctx.platformError ?? 'The platform catalog has not loaded.'}</p>
                {/if}
            </div>
        </div>
        <div class="panel">
            <div class="hd"><h2>About</h2></div>
            <div class="bd">
                <dl class="kv sm">
                    <dt>Edition</dt>
                    <dd>Community edition</dd>
                    <dt>Console version</dt>
                    <dd><code>{s?.version ?? ''}</code></dd>
                    <dt>Decisions</dt>
                    <dd>ADR 0007 control plane and builder · ADR 0010 editions</dd>
                </dl>
            </div>
        </div>
    </div>
</section>
