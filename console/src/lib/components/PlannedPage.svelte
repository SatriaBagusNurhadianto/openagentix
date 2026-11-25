<script lang="ts">
    import { segments, type PlannedContent } from '$lib/planned';
    import Notice from './Notice.svelte';
    import PageHeader from './PageHeader.svelte';

    interface Props {
        content: PlannedContent;
    }
    let { content }: Props = $props();
</script>

<svelte:head><title>{content.title} · OpenAgentix console</title></svelte:head>

<section class="view planned max-form">
    <PageHeader title={content.title} strip={content.strip} />
    <Notice kind="ro" title={content.noticeTitle}>
        <p class="sm">
            {#each segments(content.notice) as seg, i (i)}{#if seg.code}<code>{seg.text}</code
                    >{:else}{seg.text}{/if}{/each}
        </p>
    </Notice>
    <div class="grid g2">
        <div class="panel">
            <div class="hd"><h2>What this page will show</h2></div>
            <div class="bd">
                <ul>
                    {#each content.willShow as line (line)}<li>
                            {#each segments(line) as seg, i (i)}{#if seg.code}<code>{seg.text}</code
                                    >{:else}{seg.text}{/if}{/each}
                        </li>{/each}
                </ul>
            </div>
        </div>
        <div class="panel">
            <div class="hd"><h2>Why it is not available yet</h2></div>
            <div class="bd stack">
                <p class="t2">
                    {#each segments(content.why) as seg, i (i)}{#if seg.code}<code>{seg.text}</code
                            >{:else}{seg.text}{/if}{/each}
                </p>
                <div>
                    <h3>Where it is configured today</h3>
                    <p class="t2 sm mt-1">
                        {#each segments(content.today) as seg, i (i)}{#if seg.code}<code>{seg.text}</code
                                >{:else}{seg.text}{/if}{/each}
                    </p>
                </div>
            </div>
        </div>
    </div>
    <p class="sm t3">
        This page shows no data on purpose: nothing is invented where the platform has no resource behind the page.
    </p>
</section>
