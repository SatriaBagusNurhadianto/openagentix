<script lang="ts">
    // A line of placeholder. The container that holds skeletons carries aria-busy and a hidden "Loading".
    // The width is a class (for example w-[140px]), never an inline style: the console's CSP forbids inline styles.
    interface Props {
        class?: string;
    }
    let { class: cls = 'w-full' }: Props = $props();
</script>

<span class="skel {cls}" aria-hidden="true"></span>
