<script lang="ts">
    // role="switch". The name comes from the element that carries the setting's label (labelledby), never "On/Off".
    interface Props {
        checked: boolean;
        labelledby: string;
        describedby?: string;
        disabled?: boolean;
        onchange?: (v: boolean) => void;
        id?: string;
    }
    let { checked = $bindable(), labelledby, describedby, disabled = false, onchange, id }: Props = $props();
    function toggle() {
        if (disabled) return;
        checked = !checked;
        onchange?.(checked);
    }
</script>

<button
    {id}
    type="button"
    role="switch"
    class="sw"
    aria-checked={checked}
    aria-labelledby={labelledby}
    aria-describedby={describedby}
    aria-disabled={disabled ? 'true' : undefined}
    onclick={toggle}
></button>
