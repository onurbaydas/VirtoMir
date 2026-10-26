<script lang="ts">
  import { t } from '../i18n.svelte'
  // Running image uploads, on every page (they keep going when you leave Storage).
  import Upload from '@lucide/svelte/icons/upload'
  import X from '@lucide/svelte/icons/x'
  import { uploads } from '../uploads.svelte'
</script>

{#if uploads.list.length}
  <div class="uploads" role="status" aria-live="polite">
    {#each uploads.list as u (u.id)}
      <div class="row">
        <Upload size={14} />
        <span class="name mono" title={t('{name} into {pool}', { name: u.name, pool: u.pool })}>{u.name}</span>
        <span class="bar"><span style:width="{u.fraction * 100}%"></span></span>
        <span class="pct num">{Math.round(u.fraction * 100)} %</span>
        <button class="x" onclick={() => uploads.cancel(u.id)} title={t('Cancel the upload')} aria-label={t('Cancel the upload')}><X size={13} /></button>
      </div>
    {/each}
  </div>
{/if}

<style>
  .uploads {
    position: fixed;
    left: 16px;
    bottom: 16px;
    z-index: 60;
    display: grid;
    gap: 6px;
    width: min(360px, calc(100vw - 32px));
    padding: 8px 10px;
    border: var(--frame) solid var(--ink);
    background: var(--surface);
    box-shadow: 3px 3px 0 var(--ink);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
  }
  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bar {
    flex: none;
    width: 90px;
    height: 6px;
    border: 1px solid var(--border-strong);
    background: var(--surface-2);
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .pct {
    width: 38px;
    text-align: right;
  }
  .x {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border: 1px solid var(--border-strong);
    background: var(--metal);
    color: var(--text);
    cursor: pointer;
  }
</style>
