<script lang="ts">
    import type { HTMLButtonAttributes } from 'svelte/elements';
    import Icon from './Icon.svelte';

    interface Props extends Omit<HTMLButtonAttributes, 'class' | 'children'> {
        icon: string;
        label: string;
        class?: string;
        iconSize?: number;
    }
    let { icon, label, class: cls = '', iconSize, ...rest }: Props = $props();
</script>

<button type="button" {...rest} class="ib {cls}" aria-label={label} title={label}
    ><Icon name={icon} size={iconSize} /></button
>
