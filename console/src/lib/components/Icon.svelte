<script lang="ts">
    // An icon from the sprite (IconSprite.svelte). Decorative: its meaning is in the text next to it.
    interface Props {
        name: string;
        class?: string;
        size?: number;
    }
    let { name, class: cls = '', size }: Props = $props();
</script>

<svg class="i {cls}" aria-hidden="true" focusable="false" width={size} height={size}><use href="#{name}" /></svg>
