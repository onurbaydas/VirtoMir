<script lang="ts">
  import { session } from '../session.svelte'
  // The common settings as a form. Form and XML editor read and write the same persistent
  // definition; "Review changes" shows exactly what the form will change in the XML.
  import ArrowUp from '@lucide/svelte/icons/arrow-up'
  import ArrowDown from '@lucide/svelte/icons/arrow-down'
  import Save from '@lucide/svelte/icons/save'
  import Undo2 from '@lucide/svelte/icons/undo-2'
  import FileDiff from '@lucide/svelte/icons/file-diff'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import { api, errorText, type Settings, type SettingsView } from '../api'
  import { diffLines, hunks, type DiffLine } from '../diff'
  import { live } from '../live.svelte'
  import { formatKiB } from '../format'
  import { toasts } from '../toast.svelte'
  import { cluster } from '../cluster.svelte'
  // The machine's owner (quota self-service): live list entry for this machine.
  let owner = $derived(live.snapshot?.domains.find((d) => d.uuid === uuid))

  let { uuid, active, onsaved }: { uuid: string; active: boolean; onsaved: () => void } = $props()

  let view = $state<SettingsView | null>(null)
  let form = $state<Settings | null>(null)
  let loadErr = $state('')
  let saving = $state(false)
  let saveErr = $state('')
  let diff = $state<(DiffLine | null)[] | null>(null)
  let diffBusy = $state(false)

  async function load() {
    try {
      const v = await api.settings(uuid)
      view = v
      form = pick(v)
      loadErr = ''
      diff = null
    } catch (e) {
      loadErr = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  function pick(v: SettingsView): Settings {
    const { bootDevices: _b, hotplugMemoryMiB: _h, ...s } = v
    return { ...s, bootOrder: [...s.bootOrder], pinning: [...(s.pinning ?? [])] }
  }

  // ---- Performance tuning (pinning, huge pages, NUMA) -------------------------------------
  let tuningOpen = $state(false)
  // Pinning has one entry per vCPU and follows the vCPU count.
  $effect(() => {
    if (!form) return
    const n = form.maxVcpus
    if (form.pinning.length !== n) form.pinning = Array.from({ length: n }, (_, i) => form!.pinning[i] ?? '')
  })
  let pinned = $derived(form ? form.pinning.filter((p) => p).length : 0)
  let tuned = $derived(!!form && (pinned > 0 || !!form.emulatorPin || form.hugePages || !!form.numaMode))
  // One host CPU per vCPU, starting at `first` — the usual setup for latency-sensitive guests.
  let pinFrom = $state(0)
  function pinOneToOne() {
    if (!form) return
    form.pinning = form.pinning.map((_, i) => String(pinFrom + i))
  }
  const CPUSET = /^\^?[0-9]+(-[0-9]+)?(,\^?[0-9]+(-[0-9]+)?)*$/
  const badSet = (s: string) => !!s && !CPUSET.test(s)

  let dirty = $derived(!!form && !!view && JSON.stringify(form) !== JSON.stringify(pick(view)))
  let hostThreads = $derived(live.host?.cpus ?? 0)
  let hostMemMiB = $derived(Math.floor((live.host?.memoryKiB ?? 0) / 1024))

  // Topology follows the vCPU count unless the user shapes it: 1 socket × N cores × 1 thread.
  function setMaxVcpus(n: number) {
    if (!form) return
    form.maxVcpus = n
    if (form.sockets * form.cores * form.threads !== n) {
      form.sockets = 1
      form.cores = n
      form.threads = 1
    }
    if (form.vcpus > n) form.vcpus = n
  }

  let problems = $derived.by(() => {
    if (!form) return [] as string[]
    const p: string[] = []
    if (form.sockets * form.cores * form.threads !== form.maxVcpus)
      p.push(`Sockets × cores × threads (${form.sockets * form.cores * form.threads}) must equal the maximum vCPUs (${form.maxVcpus}).`)
    if (form.vcpus < 1 || form.vcpus > form.maxVcpus) p.push('Active vCPUs must be between 1 and the maximum.')
    if (form.cpuMode === 'custom' && !form.cpuModel?.trim()) p.push('Name the CPU model for custom mode.')
    if (form.currentMemoryMiB > form.memoryMiB) p.push('Current memory cannot be larger than the assigned memory.')
    if (form.memoryMiB < 64) p.push('Memory must be at least 64 MiB.')
    if (form.firmware === 'bios' && form.secureBoot) p.push('Secure Boot needs UEFI.')
    const bad = form.pinning.findIndex(badSet)
    if (bad >= 0) p.push(`vCPU ${bad}: use host CPU numbers and ranges like 2, 4-5 or 0,2.`)
    if (badSet(form.emulatorPin)) p.push('Emulator threads: use host CPU numbers and ranges.')
    if (form.numaMode && (!form.numaNodes || badSet(form.numaNodes))) p.push('Name the NUMA nodes memory may come from, e.g. 0.')
    return p
  })
  let firmwareChanged = $derived(!!form && !!view && (form.firmware !== view.firmware || form.secureBoot !== view.secureBoot))

  // Boot order editing.
  let unused = $derived(view && form ? view.bootDevices.filter((d) => !form!.bootOrder.includes(d.key)) : [])
  const label = (key: string) => view?.bootDevices.find((d) => d.key === key)?.label ?? key
  function move(i: number, by: number) {
    if (!form) return
    const o = form.bootOrder
    const j = i + by
    if (j < 0 || j >= o.length) return
    ;[o[i], o[j]] = [o[j], o[i]]
  }

  async function review() {
    if (!form) return
    diffBusy = true
    saveErr = ''
    try {
      const p = await api.previewSettings(uuid, $state.snapshot(form))
      diff = hunks(diffLines(p.before, p.after))
    } catch (e) {
      saveErr = errorText(e)
    } finally {
      diffBusy = false
    }
  }

  async function save() {
    if (!form) return
    saving = true
    saveErr = ''
    try {
      const res = await api.saveSettings(uuid, $state.snapshot(form))
      const liveNote = res.appliedLive.length ? `Applied now: ${res.appliedLive.join(', ')}. ` : ''
      toasts.push({
        kind: res.liveErrors.length ? 'warn' : 'info',
        title: 'Settings saved',
        detail:
          liveNote +
          (res.liveErrors.length ? `Not applied live (next boot): ${res.liveErrors.join('; ')}` : res.appliesOnNextBoot ? 'Other changes take effect at the next boot.' : ''),
      })
      await load()
      onsaved()
    } catch (e) {
      saveErr = errorText(e)
    } finally {
      saving = false
    }
  }

  const MODES = [
    { key: 'host-passthrough', label: 'Same as host', hint: 'Fastest; the guest sees your real CPU. Live migration only to hosts with the identical CPU.' },
    { key: 'host-model', label: 'Closest to host', hint: 'Nearly as fast, a bit more portable.' },
    { key: 'maximum', label: 'Everything supported', hint: 'All features the hypervisor can offer.' },
    { key: 'custom', label: 'Named model', hint: 'A fixed CPU model, e.g. for migration across different hosts.' },
  ] as const

  // A model every host of the cluster runs, so the machine can be live-migrated anywhere.
  let baselineBusy = $state(false)
  async function useBaseline() {
    if (!form) return
    baselineBusy = true
    try {
      const b = await api.cpuBaseline()
      form.cpuMode = 'custom'
      form.cpuModel = b.model
      toasts.push({ kind: 'info', title: `${b.model} runs on all ${b.hosts} hosts`, detail: 'Save, then restart the machine for it to take effect.' })
    } catch (e) {
      toasts.push({ kind: 'error', title: 'No common CPU model', detail: errorText(e) })
    } finally {
      baselineBusy = false
    }
  }
</script>

{#if loadErr}
  <div class="ui-error-box">{loadErr}</div>
{:else if !form || !view}
  <p class="muted">Loading…</p>
{:else}
  <div class="settings">
    {#if active}
      <div class="note">
        <TriangleAlert size={15} />
        <span>The machine is running. Active vCPUs and current memory are applied immediately when possible; everything else at the next boot.</span>
      </div>
    {/if}

    <section class="ui-panel card">
      <h2>Processor</h2>
      <div class="grid3">
        <label class="ui-field">
          <span class="label">Maximum vCPUs</span>
          <input class="ui-input" type="number" min="1" max="512" value={form.maxVcpus} oninput={(e) => setMaxVcpus(+e.currentTarget.value)} />
          <span class="hint">Host has {hostThreads} threads.</span>
        </label>
        <label class="ui-field">
          <span class="label">Active at boot</span>
          <input class="ui-input" type="number" min="1" max={form.maxVcpus} bind:value={form.vcpus} />
          <span class="hint">Fewer than maximum leaves room to hot-plug.</span>
        </label>
        <div class="ui-field">
          <span class="label">Topology</span>
          <div class="topo">
            <input class="ui-input" type="number" min="1" bind:value={form.sockets} aria-label="Sockets" title="Sockets" />
            <span>×</span>
            <input class="ui-input" type="number" min="1" bind:value={form.cores} aria-label="Cores" title="Cores per socket" />
            <span>×</span>
            <input class="ui-input" type="number" min="1" bind:value={form.threads} aria-label="Threads" title="Threads per core" />
          </div>
          <span class="hint">sockets × cores × threads</span>
        </div>
      </div>
      <div class="modes" role="radiogroup" aria-label="CPU model">
        {#each MODES as m (m.key)}
          <label class="mode" class:on={form.cpuMode === m.key} title={m.hint}>
            <input type="radio" name="cpumode" value={m.key} bind:group={form.cpuMode} />
            <strong>{m.label}</strong>
            <span class="mono">{m.key}</span>
          </label>
        {/each}
      </div>
      {#if form.cpuMode === 'custom'}
        <label class="ui-field narrow">
          <span class="label">CPU model</span>
          <input class="ui-input mono" bind:value={form.cpuModel} placeholder="Skylake-Client, EPYC-Rome, Westmere…" />
        </label>
      {/if}
      {#if cluster.clustered}
        <button class="ui-btn sm baseline" class:busy={baselineBusy} disabled={baselineBusy || !session.can('admin')} onclick={useBaseline}>
          Use a model every cluster host supports
        </button>
      {/if}
    </section>

    <section class="ui-panel card">
      <h2>Memory</h2>
      <div class="grid3">
        <label class="ui-field">
          <span class="label">Assigned (MiB)</span>
          <input class="ui-input" type="number" min="64" step="256" bind:value={form.memoryMiB} />
          <span class="hint">{formatKiB(form.memoryMiB * 1024)} of the host's {formatKiB(hostMemMiB * 1024)}{view.hotplugMemoryMiB ? ` · hot-plug max ${formatKiB(view.hotplugMemoryMiB * 1024)}` : ''}</span>
        </label>
        <label class="ui-field">
          <span class="label">Current (MiB)</span>
          <input class="ui-input" type="number" min="64" max={form.memoryMiB} step="256" bind:value={form.currentMemoryMiB} />
          <span class="hint">Balloon target: the guest gives memory back below the assigned amount.</span>
        </label>
        <div class="ui-field">
          <span class="label">Quick</span>
          <div class="chips">
            {#each [1, 2, 4, 8, 16] as g (g)}
              <button type="button" class="chip-btn" class:on={form.memoryMiB === g * 1024} onclick={() => form && ((form.memoryMiB = g * 1024), (form.currentMemoryMiB = g * 1024))}>{g} GiB</button>
            {/each}
          </div>
        </div>
      </div>
    </section>

    <section class="ui-panel card">
      <button class="fold" type="button" aria-expanded={tuningOpen || tuned} onclick={() => (tuningOpen = !tuningOpen)}>
        <h2>Performance tuning</h2>
        <span class="hint">{tuned ? `${pinned ? `${pinned} pinned vCPU${pinned > 1 ? 's' : ''}` : 'no pinning'}${form.hugePages ? ' · huge pages' : ''}${form.numaMode ? ` · NUMA ${form.numaMode} ${form.numaNodes}` : ''}` : 'Default: the host schedules everything. For latency-sensitive guests.'}</span>
      </button>
      {#if tuningOpen || tuned}
        <div class="tuning">
          <div>
            <span class="label">vCPU pinning <span class="hint">— host CPUs each vCPU may run on (this host has {hostThreads || '?'}: 0–{Math.max(0, hostThreads - 1)})</span></span>
            <div class="pins">
              {#each form.pinning as _, i (i)}
                <label class="pin">
                  <span class="mono">vCPU {i}</span>
                  <input class="ui-input mono" bind:value={form.pinning[i]} placeholder="any" aria-invalid={badSet(form.pinning[i])} />
                </label>
              {/each}
            </div>
            <div class="pin-tools">
              <span class="hint">One host CPU each, starting at</span>
              <input class="ui-input mono tiny" type="number" min="0" max={Math.max(0, hostThreads - 1)} bind:value={pinFrom} />
              <button class="ui-btn sm" type="button" onclick={pinOneToOne}>Pin 1:1</button>
              <button class="ui-btn sm ghost" type="button" onclick={() => form && (form.pinning = form.pinning.map(() => ''))}>Clear</button>
            </div>
          </div>
          <div class="grid2">
            <label class="ui-field">
              <span class="label">Emulator threads on</span>
              <input class="ui-input mono" bind:value={form.emulatorPin} placeholder="any (e.g. 0-1)" aria-invalid={badSet(form.emulatorPin)} />
              <span class="hint">QEMU's own work (I/O, timers) — keep it off the pinned vCPUs' cores.</span>
            </label>
            <label class="ui-field">
              <span class="label">Memory from NUMA nodes</span>
              <div class="numa">
                <select class="ui-input" bind:value={form.numaMode}>
                  <option value="">Any node (default)</option>
                  <option value="strict">Only these (strict)</option>
                  <option value="preferred">Prefer this one</option>
                  <option value="interleave">Spread over these</option>
                  <option value="restrictive">Only these, movable</option>
                </select>
                {#if form.numaMode}<input class="ui-input mono" bind:value={form.numaNodes} placeholder="0" aria-invalid={badSet(form.numaNodes) || !form.numaNodes} />{/if}
              </div>
              <span class="hint">This host has {live.host?.numaNodes ?? '?'} NUMA node{live.host?.numaNodes === 1 ? '' : 's'}.</span>
            </label>
          </div>
          <label class="ui-switch"><input type="checkbox" bind:checked={form.hugePages} /> Back memory with huge pages <span class="hint">— faster for big guests; the host must reserve them (vm.nr_hugepages), or the machine will not start</span></label>
          <span class="hint">Takes effect at the next start.</span>
        </div>
      {/if}
    </section>

    <section class="ui-panel card">
      <h2>Boot</h2>
      <ol class="boot">
        {#each form.bootOrder as key, i (key)}
          <li>
            <span class="n">{i + 1}</span>
            <span class="bl">{label(key)}</span>
            <button type="button" class="ui-btn sm ghost" aria-label="Move up" disabled={i === 0} onclick={() => move(i, -1)}><ArrowUp size={14} /></button>
            <button type="button" class="ui-btn sm ghost" aria-label="Move down" disabled={i === form.bootOrder.length - 1} onclick={() => move(i, 1)}><ArrowDown size={14} /></button>
            <button type="button" class="ui-btn sm ghost" onclick={() => form && (form.bootOrder = form.bootOrder.filter((k) => k !== key))}>Remove</button>
          </li>
        {/each}
        {#if form.bootOrder.length === 0}<li class="empty">No boot device — the firmware decides.</li>{/if}
      </ol>
      {#if unused.length}
        <div class="add-boot">
          {#each unused as d (d.key)}
            <button type="button" class="chip-btn" onclick={() => form && form.bootOrder.push(d.key)}>+ {d.label}</button>
          {/each}
        </div>
      {/if}
      <label class="ui-switch"><input type="checkbox" bind:checked={form.bootMenu} /> Show the boot menu (press Esc/F12 at start)</label>
    </section>

    <section class="ui-panel card">
      <h2>Firmware</h2>
      <div class="grid3">
        <label class="ui-field">
          <span class="label">Firmware</span>
          <select class="ui-input" bind:value={form.firmware}>
            <option value="uefi">UEFI</option>
            <option value="bios">BIOS (legacy)</option>
          </select>
        </label>
        <label class="ui-switch fw-sb"><input type="checkbox" bind:checked={form.secureBoot} disabled={form.firmware === 'bios'} /> Secure Boot</label>
      </div>
      {#if firmwareChanged}
        <div class="warn-box">
          <TriangleAlert size={15} />
          <span>Changing firmware resets the UEFI variables (boot entries), and an installed system usually only boots with the firmware it was installed with. Take a snapshot first.</span>
        </div>
      {/if}
    </section>

    <section class="ui-panel card">
      <h2>Name tag</h2>
      <div class="grid2">
        <label class="ui-field">
          <span class="label">Title</span>
          <input class="ui-input" bind:value={form.title} placeholder="Short description shown next to the name" />
        </label>
        <label class="ui-field">
          <span class="label">Description</span>
          <textarea class="ui-input" rows="2" bind:value={form.description} placeholder="Notes for yourself and your team"></textarea>
        </label>
      </div>
    </section>

    {#if diff}
      <section class="ui-panel diff mono" aria-label="XML changes">
        <header>What changes in the XML</header>
        {#if diff.every((l) => l === null || l.kind === 'same')}
          <p class="muted">No changes.</p>
        {:else}
          {#each diff as l, i (i)}
            {#if l === null}<div class="gap">⋯</div>{:else}<div class="dl {l.kind}"><span class="sign">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span>{l.text}</div>{/if}
          {/each}
        {/if}
      </section>
    {/if}

    <footer class="ui-panel bar">
      <span class="status" class:bad={problems.length > 0}>
        {problems[0] ?? (dirty ? 'Unsaved changes' : 'Everything is saved')}
      </span>
      <button class="ui-btn ghost" onclick={() => view && ((form = pick(view)), (diff = null))} disabled={!dirty || saving}><Undo2 size={14} /> Reset</button>
      <button class="ui-btn" onclick={review} disabled={!dirty || problems.length > 0 || diffBusy}><FileDiff size={14} /> {diffBusy ? 'Comparing…' : 'Review changes'}</button>
      <button class="ui-btn primary" onclick={save} disabled={!dirty || problems.length > 0 || saving || !session.canManage(owner)} title={session.whyManage(owner)}><Save size={14} /> {saving ? 'Saving…' : 'Save'}</button>
    </footer>
    {#if saveErr}<div class="ui-error-box" role="alert">{saveErr}</div>{/if}
  </div>
{/if}

<style>
  .baseline {
    margin-top: 10px;
  }
  .settings {
    display: grid;
    gap: 14px;
  }
  .note,
  .warn-box {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px 14px;
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--warn) 10%, var(--surface));
    border: 1px solid color-mix(in srgb, var(--warn) 35%, transparent);
    color: var(--warn);
    font-size: 13px;
  }
  .note span,
  .warn-box span {
    color: var(--text-2);
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 16px 18px;
  }
  h2 {
    margin: 0;
    font-size: 14.5px;
    font-weight: 600;
  }
  .fold {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    padding: 0;
    border: none;
    background: none;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .tuning {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .pins {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
    gap: 8px;
    margin-top: 6px;
  }
  .pin {
    display: flex;
    flex-direction: column;
    gap: 3px;
    font-size: 12px;
    color: var(--text-3);
  }
  .pin-tools {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 8px;
  }
  .tiny {
    width: 70px;
  }
  .numa {
    display: flex;
    gap: 8px;
  }
  .numa input {
    width: 90px;
  }
  .grid3 {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 14px;
    align-items: start;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 2fr;
    gap: 14px;
  }
  .narrow {
    max-width: 360px;
  }
  .topo {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-3);
  }
  .topo input {
    min-width: 0;
  }
  .modes {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 8px;
  }
  .mode {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    cursor: pointer;
  }
  .mode input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .mode.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .mode:focus-within {
    outline: 2px solid var(--accent);
  }
  .mode strong {
    font-size: 13px;
  }
  .mode span {
    color: var(--text-3);
    font-size: 11px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip-btn {
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border-strong);
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 12.5px;
    cursor: pointer;
  }
  .chip-btn:hover {
    color: var(--text);
  }
  .chip-btn.on {
    border-color: var(--accent);
    color: var(--accent);
    background: var(--accent-soft);
  }
  .boot {
    margin: 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 6px;
  }
  .boot li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font-size: 13px;
  }
  .boot li.empty {
    color: var(--text-3);
  }
  .n {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent);
    font-size: 11.5px;
    font-weight: 700;
  }
  .bl {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .add-boot {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .fw-sb {
    margin-top: 28px;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .muted {
    color: var(--text-3);
  }
  .diff {
    padding: 0;
    overflow: hidden;
    font-size: 12px;
    line-height: 1.6;
  }
  .diff header {
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
    font-family: var(--font-sans);
    font-weight: 600;
    font-size: 13px;
  }
  .diff p {
    padding: 0 14px;
  }
  .dl {
    padding: 0 14px;
    white-space: pre;
    overflow-x: auto;
    color: var(--text-3);
  }
  .dl.add {
    background: color-mix(in srgb, var(--ok) 12%, transparent);
    color: var(--text);
  }
  .dl.del {
    background: var(--danger-soft);
    color: var(--text-2);
    text-decoration: line-through;
    text-decoration-color: color-mix(in srgb, var(--danger) 50%, transparent);
  }
  .sign {
    display: inline-block;
    width: 16px;
    color: var(--text-3);
  }
  .gap {
    padding: 0 14px;
    color: var(--text-3);
    background: var(--surface-2);
  }
  .bar {
    position: sticky;
    bottom: 12px;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    box-shadow: var(--shadow-pop);
  }
  .status {
    flex: 1;
    min-width: 0;
    color: var(--text-3);
    font-size: 13px;
  }
  .status.bad {
    color: var(--danger);
  }
  @media (max-width: 900px) {
    .grid3,
    .grid2 {
      grid-template-columns: 1fr;
    }
    .modes {
      grid-template-columns: 1fr 1fr;
    }
    .fw-sb {
      margin-top: 0;
    }
    .bar {
      flex-wrap: wrap;
    }
  }
</style>
