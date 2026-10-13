<script lang="ts">
  // Label/value rows. The libvirt XML name of each value appears on hover or focus, so
  // beginners read plain words and power users can still map everything back to the XML.
  import type { Snippet } from 'svelte'
  import type { Prop } from '../api'

  let { props, children }: { props: Prop[]; children?: Snippet } = $props()
</script>

<dl>
  {#each props as p, i (p.label + i)}
    <div class="row" tabindex="-1">
      <dt>
        {p.label}
        {#if p.tech}<span class="tech mono">{p.tech}</span>{/if}
      </dt>
      <dd class:mono={p.value.startsWith('/') || /^[0-9a-f]{2}(:[0-9a-f]{2}){5}$/i.test(p.value)}>{p.value}</dd>
    </div>
  {/each}
  {@render children?.()}
</dl>

<style>
  dl {
    margin: 0;
    display: grid;
    gap: 0;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(130px, 38%) 1fr;
    gap: 16px;
    padding: 7px 0;
    border-top: 1px solid var(--border);
    outline: none;
  }
  .row:first-child {
    border-top: 0;
  }
  dt {
    display: flex;
    flex-direction: column;
    color: var(--text-3);
    font-size: 13px;
  }
  .tech {
    max-height: 0;
    overflow: hidden;
    opacity: 0;
    color: var(--accent);
    font-size: 11px;
    transition:
      max-height var(--dur) var(--ease-out),
      opacity var(--dur) var(--ease-out);
  }
  .row:hover .tech,
  .row:focus-within .tech {
    max-height: 18px;
    opacity: 0.85;
  }
  dd {
    margin: 0;
    color: var(--text);
    font-size: 13.5px;
    overflow-wrap: anywhere;
  }
  dd.mono {
    font-size: 12.5px;
  }
  @media (max-width: 520px) {
    .row {
      grid-template-columns: 1fr;
      gap: 2px;
    }
  }
</style>
