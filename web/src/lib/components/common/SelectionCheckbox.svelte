<script lang="ts">
  import { Checkbox } from '@kenn-io/kit-ui';

  interface Props {
    checked: boolean;
    label: string;
    mixed?: boolean;
    disabled?: boolean;
    onToggle: (range: boolean) => void;
  }

  let {
    checked,
    label,
    mixed = false,
    disabled = false,
    onToggle
  }: Props = $props();

  let range = false;
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions --
     The nested Kit checkbox owns keyboard semantics; this wrapper only keeps
     Shift and pointer events from reaching the selectable row beneath it. -->
<span
  class="selection-checkbox"
  onpointerdown={(event) => {
    range = event.shiftKey;
    event.stopPropagation();
  }}
  onclick={(event) => {
    range = event.shiftKey;
    event.stopPropagation();
  }}
>
  <Checkbox
    {checked}
    indeterminate={mixed}
    {disabled}
    ariaLabel={label}
    onchange={() => onToggle(range)}
  />
</span>

<style>
  .selection-checkbox {
    display: grid;
    width: 16px;
    height: 16px;
    flex: none;
    place-items: center;
  }

  .selection-checkbox :global(.kit-checkbox) {
    width: 16px;
    height: 16px;
  }
</style>
