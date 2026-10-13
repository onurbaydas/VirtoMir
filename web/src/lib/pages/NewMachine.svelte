<script lang="ts">
  import { session } from '../session.svelte'
  import ArrowLeft from '@lucide/svelte/icons/arrow-left'
  import Disc from '@lucide/svelte/icons/disc'
  import Cpu from '@lucide/svelte/icons/cpu'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Network from '@lucide/svelte/icons/network'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import Rocket from '@lucide/svelte/icons/rocket'
  import { api, errorText, type CreateSpec, type Pool, type VNetwork, type Volume } from '../api'
  import { live } from '../live.svelte'
  import { formatBytes, formatKiB } from '../format'
  import { machineHref, router } from '../router.svelte'
  import { toasts } from '../toast.svelte'

  type OS = CreateSpec['os']
  const OSES: { key: OS; label: string; hint: string }[] = [
    { key: 'linux', label: 'Linux', hint: 'Debian, Ubuntu, Fedora, Arch… Fast VirtIO devices.' },
    { key: 'windows', label: 'Windows', hint: 'Windows 10/11 and Server. UEFI, Secure Boot and TPM 2.0.' },
    { key: 'other', label: 'Other', hint: 'BSD, older systems. Widely compatible devices.' },
  ]
  const DEFAULTS: Record<OS, { vcpus: number; memGiB: number; diskGiB: number; secureBoot: boolean; tpm: boolean }> = {
    linux: { vcpus: 2, memGiB: 2, diskGiB: 20, secureBoot: false, tpm: false },
    windows: { vcpus: 4, memGiB: 8, diskGiB: 64, secureBoot: true, tpm: true },
    other: { vcpus: 2, memGiB: 2, diskGiB: 20, secureBoot: false, tpm: false },
  }

  let name = $state('')
  let nameTouched = $state(false)
  let os = $state<OS>('linux')
  let vcpus = $state(2)
  let memGiB = $state(2)
  let withDisk = $state(true)
  let diskGiB = $state(20)
  let pool = $state('default')
  let iso = $state('')
  // Where the system comes from: an installer ISO, a ready cloud image, or an existing disk.
  let source = $state<'iso' | 'cloud' | 'disk'>('iso')
  let baseImage = $state('')
  let existingDisk = $state('')
  let ciUser = $state('admin')
  let ciPassword = $state('')
  let ciKeys = $state('')
  let netChoice = $state('network:default') // "network:<name>" | "bridge" | "none"
  let bridge = $state('br0')
  let firmware = $state<'uefi' | 'bios'>('uefi')
  let secureBoot = $state(false)
  let tpm = $state(false)
  let start = $state(true)
  let advanced = $state(false)

  function pickOS(k: OS) {
    os = k
    const d = DEFAULTS[k]
    vcpus = Math.min(d.vcpus, maxCPUs)
    // Never suggest more than ¾ of the host's memory.
    memGiB = Math.max(1, Math.min(d.memGiB, Math.floor(maxMemGiB * 0.75)))
    diskGiB = d.diskGiB
    secureBoot = d.secureBoot
    tpm = d.tpm
    if (k === 'windows') firmware = 'uefi'
  }

  // ---- Host resources and choices -----------------------------------------------------
  let maxCPUs = $derived(Math.max(1, live.host?.cpus ?? 8))
  let maxMemGiB = $derived(Math.max(1, Math.floor((live.host?.memoryKiB ?? 8 << 20) / (1 << 20))))

  let pools = $state<Pool[]>([])
  let isos = $state<{ pool: string; vol: Volume }[]>([])
  /** Unused disk images: candidates for cloud images and "use existing disk". */
  let images = $state<{ pool: string; vol: Volume }[]>([])
  let networks = $state<VNetwork[]>([])
  let loadErr = $state('')

  $effect(() => {
    void (async () => {
      try {
        const [p, n] = await Promise.all([api.pools(), api.networks()])
        pools = p.filter((x) => x.active)
        networks = n.filter((x) => x.active)
        if (!pools.some((x) => x.name === pool) && pools[0]) pool = pools[0].name
        if (!networks.some((x) => x.name === 'default')) netChoice = networks[0] ? `network:${networks[0].name}` : 'none'
        const lists = await Promise.all(pools.map(async (p) => ({ p: p.name, v: await api.volumes(p.name).catch(() => []) })))
        isos = lists.flatMap(({ p, v }) => v.filter((x) => x.format === 'iso').map((vol) => ({ pool: p, vol })))
        images = lists.flatMap(({ p, v }) =>
          v.filter((x) => x.format !== 'iso' && x.usedBy.length === 0 && !x.name.endsWith('-cidata.iso')).map((vol) => ({ pool: p, vol })),
        )
      } catch (e) {
        loadErr = errorText(e)
      }
    })()
  })

  // Suggest a name from the image ("debian-13.1.0-amd64-netinst.iso" → "debian-13").
  $effect(() => {
    const from = source === 'iso' ? iso : source === 'cloud' ? baseImage : existingDisk
    if (nameTouched || !from) return
    const base = from.split('/').pop() ?? ''
    const m = /^([a-z]+)[-_]?(\d+)?/i.exec(base)
    if (m) name = uniqueName(m[2] ? `${m[1]}-${m[2]}`.toLowerCase() : m[1].toLowerCase())
  })
  function uniqueName(base: string) {
    const taken = new Set((live.snapshot?.domains ?? []).map((d) => d.name))
    if (!taken.has(base)) return base
    for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`
  }

  let selectedPool = $derived(pools.find((p) => p.name === pool))
  let diskTooBig = $derived(!!selectedPool && withDisk && diskGiB * 2 ** 30 > selectedPool.availableBytes)
  let nameValid = $derived(/^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/.test(name))
  let nameTaken = $derived((live.snapshot?.domains ?? []).some((d) => d.name === name))
  let baseCapGiB = $derived(Math.ceil((images.find((i) => i.vol.path === baseImage)?.vol.capacityBytes ?? 0) / 2 ** 30))
  let keys = $derived(ciKeys.split('\n').map((k) => k.trim()).filter(Boolean))
  let ciProblem = $derived(
    source !== 'cloud'
      ? ''
      : !/^[a-z_][a-z0-9_-]{0,31}$/.test(ciUser)
        ? 'User name: lowercase letters, digits, "_" or "-".'
        : !ciPassword && keys.length === 0
          ? 'Set a password or an SSH key to be able to sign in.'
          : '',
  )
  let bootable = $derived(source === 'iso' ? withDisk || !!iso : source === 'cloud' ? !!baseImage : !!existingDisk)
  let canCreate = $derived(nameValid && !nameTaken && bootable && !ciProblem && vcpus >= 1 && memGiB > 0)

  let busy = $state(false)
  let error = $state('')

  async function create(e: SubmitEvent) {
    e.preventDefault()
    if (!canCreate) return
    busy = true
    error = ''
    const spec: CreateSpec = {
      name,
      os,
      vcpus,
      memoryMiB: Math.round(memGiB * 1024),
      firmware,
      secureBoot: firmware === 'uefi' && secureBoot,
      tpm,
      pool,
      diskGiB: source === 'cloud' ? Math.max(diskGiB, baseCapGiB) : source === 'iso' && withDisk ? diskGiB : 0,
      iso: source === 'iso' ? iso || undefined : undefined,
      baseImage: source === 'cloud' ? baseImage : undefined,
      diskPath: source === 'disk' ? existingDisk : undefined,
      cloudInit: source === 'cloud' ? { user: ciUser, password: ciPassword || undefined, sshKeys: keys } : undefined,
      network: netChoice.startsWith('network:')
        ? { network: netChoice.slice(8) }
        : netChoice === 'bridge'
          ? { bridge }
          : undefined,
      start,
    }
    try {
      const res = await api.create(spec)
      if (res.startError) toasts.push({ kind: 'warn', title: `${name} was created but did not start`, detail: res.startError })
      else toasts.push({ kind: 'info', title: start ? `${name} is starting` : `${name} created` })
      router.navigate(machineHref(res.uuid))
    } catch (err) {
      error = errorText(err)
    } finally {
      busy = false
    }
  }
</script>

<svelte:head><title>New machine · VirtoMir</title></svelte:head>

<a class="back" href="/" onclick={router.link}><ArrowLeft size={15} /> Overview</a>
<header class="page-head">
  <div>
    <h1>New machine</h1>
    <p class="lead">Pick an operating system and installation media; everything else has a sensible default.</p>
  </div>
</header>

{#if loadErr}<div class="ui-error-box">{loadErr}</div>{/if}

<form class="layout" onsubmit={create}>
  <div class="steps">
    <section class="ui-panel step">
      <h2><span class="n">1</span> Name and system</h2>
      <label class="ui-field">
        <span class="label">Name</span>
        <input
          class="ui-input"
          bind:value={name}
          oninput={() => (nameTouched = true)}
          placeholder="web-server"
          aria-invalid={name !== '' && (!nameValid || nameTaken)}
          spellcheck="false"
        />
        {#if name && !nameValid}<span class="error">Letters, digits, '.', '_', '+' and '-', starting with a letter or digit.</span>
        {:else if nameTaken}<span class="error">A machine with this name already exists.</span>{/if}
      </label>
      <div class="choices" role="radiogroup" aria-label="Operating system">
        {#each OSES as o (o.key)}
          <label class="choice" class:on={os === o.key}>
            <input type="radio" name="os" checked={os === o.key} onchange={() => pickOS(o.key)} />
            <strong>{o.label}</strong>
            <span>{o.hint}</span>
          </label>
        {/each}
      </div>
    </section>

    <section class="ui-panel step">
      <h2><span class="n">2</span> <Disc size={16} /> Source</h2>
      <div class="seg" role="radiogroup" aria-label="Where the system comes from">
        <label class:on={source === 'iso'}><input type="radio" bind:group={source} value="iso" /> Install from ISO</label>
        <label class:on={source === 'cloud'}><input type="radio" bind:group={source} value="cloud" /> Cloud image</label>
        <label class:on={source === 'disk'}><input type="radio" bind:group={source} value="disk" /> Existing disk</label>
      </div>
      {#if source === 'iso'}
        <label class="ui-field">
          <span class="label">Boot from</span>
          <select class="ui-input" bind:value={iso}>
            <option value="">No installation media</option>
            {#each isos as i (i.vol.path)}
              <option value={i.vol.path}>{i.vol.name} — {i.pool}, {formatBytes(i.vol.capacityBytes)}</option>
            {/each}
          </select>
          <span class="hint">
            {#if isos.length === 0}No ISO images found.{/if}
            <a href="/storage" onclick={router.link}>Upload an ISO</a> in Storage.
          </span>
        </label>
      {:else if source === 'cloud'}
        <label class="ui-field">
          <span class="label">Cloud image</span>
          <select class="ui-input" bind:value={baseImage}>
            <option value="" disabled>Choose an image…</option>
            {#each images as i (i.vol.path)}
              <option value={i.vol.path}>{i.vol.name} — {i.pool}, {formatBytes(i.vol.capacityBytes)}</option>
            {/each}
          </select>
          <span class="hint">
            A ready-to-boot image (e.g. debian-13-generic-amd64.qcow2, ubuntu noble-server-cloudimg-amd64.img). It is copied, so the original stays clean.
            <a href="/storage" onclick={router.link}>Upload one</a> in Storage.
          </span>
        </label>
        <div class="ci">
          <div class="two">
            <label class="ui-field">
              <span class="label">User</span>
              <input class="ui-input mono" bind:value={ciUser} autocapitalize="off" spellcheck="false" />
            </label>
            <label class="ui-field">
              <span class="label">Password</span>
              <input class="ui-input" type="password" bind:value={ciPassword} autocomplete="new-password" placeholder="optional with an SSH key" />
            </label>
          </div>
          <label class="ui-field">
            <span class="label">SSH public keys (one per line)</span>
            <textarea class="ui-input mono" rows="2" bind:value={ciKeys} placeholder="ssh-ed25519 AAAA… you@laptop" spellcheck="false"></textarea>
            <span class="hint">cloud-init creates the user with sudo, sets the hostname to the machine name and installs the QEMU guest agent.</span>
          </label>
          {#if ciProblem}<p class="error">{ciProblem}</p>{/if}
        </div>
      {:else}
        <label class="ui-field">
          <span class="label">Disk image</span>
          <select class="ui-input" bind:value={existingDisk}>
            <option value="" disabled>Choose an unused volume…</option>
            {#each images as i (i.vol.path)}
              <option value={i.vol.path}>{i.vol.name} — {i.pool}, {formatBytes(i.vol.capacityBytes)}</option>
            {/each}
          </select>
          <span class="hint">The machine boots this image directly (not a copy) — e.g. a disk imported from another host.</span>
        </label>
      {/if}
    </section>

    <section class="ui-panel step">
      <h2><span class="n">3</span> <Cpu size={16} /> Processor and memory</h2>
      <div class="slider">
        <label for="vcpus">Virtual CPUs</label>
        <input id="vcpus" type="range" min="1" max={maxCPUs} bind:value={vcpus} />
        <input class="ui-input num-in" type="number" min="1" max="512" bind:value={vcpus} aria-label="Virtual CPUs" />
        <span class="hint">The host has {maxCPUs} threads; machines may share them.</span>
      </div>
      <div class="slider">
        <label for="mem">Memory (GiB)</label>
        <input id="mem" type="range" min="1" max={maxMemGiB} step="1" bind:value={memGiB} />
        <input class="ui-input num-in" type="number" min="0.25" step="0.25" bind:value={memGiB} aria-label="Memory in GiB" />
        <span class="hint" class:warn={memGiB > maxMemGiB * 0.8}>
          {memGiB > maxMemGiB * 0.8 ? 'Close to all host memory — the host may start swapping. ' : ''}The host has {formatKiB(live.host?.memoryKiB ?? 0)}.
        </span>
      </div>
    </section>

    <section class="ui-panel step">
      <h2><span class="n">4</span> <HardDrive size={16} /> Disk</h2>
      {#if source === 'disk'}
        <p class="hint">Uses the selected disk image as it is.</p>
      {:else if source === 'cloud'}
        <div class="two">
          <label class="ui-field">
            <span class="label">Grow to (GiB)</span>
            <input class="ui-input" type="number" min={baseCapGiB || 1} max="65536" bind:value={diskGiB} />
            <span class="hint">The image is {baseCapGiB || '?'} GiB; cloud-init grows the file system on first boot.</span>
          </label>
          <label class="ui-field">
            <span class="label">Storage pool</span>
            <select class="ui-input" bind:value={pool}>
              {#each pools as p (p.name)}<option value={p.name}>{p.name} — {formatBytes(p.availableBytes)} free</option>{/each}
            </select>
          </label>
        </div>
      {:else}
      <label class="ui-switch"><input type="checkbox" bind:checked={withDisk} /> Create a new disk</label>
      {/if}
      {#if source === 'iso' && withDisk}
        <div class="two">
          <label class="ui-field">
            <span class="label">Size (GiB)</span>
            <input class="ui-input" type="number" min="1" max="65536" bind:value={diskGiB} aria-invalid={diskTooBig} />
            <span class="hint" class:warn={diskTooBig}>
              Thin provisioned: it only uses what the guest writes.{diskTooBig ? ' Larger than the free space in the pool.' : ''}
            </span>
          </label>
          <label class="ui-field">
            <span class="label">Storage pool</span>
            <select class="ui-input" bind:value={pool}>
              {#each pools as p (p.name)}
                <option value={p.name}>{p.name} — {formatBytes(p.availableBytes)} free</option>
              {/each}
            </select>
          </label>
        </div>
      {:else if source === 'iso' && !iso}
        <p class="error">Without a disk the machine needs installation media to boot from.</p>
      {/if}
    </section>

    <section class="ui-panel step">
      <h2><span class="n">5</span> <Network size={16} /> Network</h2>
      <label class="ui-field">
        <span class="label">Connect to</span>
        <select class="ui-input" bind:value={netChoice}>
          {#each networks as n (n.name)}
            <option value="network:{n.name}">{n.name} ({n.mode === 'nat' ? 'NAT, internet through the host' : n.mode})</option>
          {/each}
          <option value="bridge">A host bridge (machine appears on your LAN)</option>
          <option value="none">No network</option>
        </select>
      </label>
      {#if netChoice === 'bridge'}
        <label class="ui-field">
          <span class="label">Bridge</span>
          <input class="ui-input mono" bind:value={bridge} placeholder="br0" />
        </label>
      {/if}
    </section>

    <section class="ui-panel step">
      <button type="button" class="adv" aria-expanded={advanced} onclick={() => (advanced = !advanced)}>
        <ShieldCheck size={16} /> Firmware and security
        <span class="adv-sum">{firmware === 'uefi' ? 'UEFI' : 'BIOS'}{secureBoot && firmware === 'uefi' ? ' · Secure Boot' : ''}{tpm ? ' · TPM 2.0' : ''}</span>
        <span class="chev" class:up={advanced}><ChevronDown size={16} /></span>
      </button>
      {#if advanced}
        <div class="adv-body">
          <div class="two">
            <label class="ui-field">
              <span class="label">Firmware</span>
              <select class="ui-input" bind:value={firmware}>
                <option value="uefi">UEFI (modern, recommended)</option>
                <option value="bios">BIOS (legacy)</option>
              </select>
            </label>
          </div>
          <label class="ui-switch"><input type="checkbox" bind:checked={secureBoot} disabled={firmware === 'bios'} /> Secure Boot</label>
          <label class="ui-switch"><input type="checkbox" bind:checked={tpm} /> TPM 2.0 (required by Windows 11)</label>
          <p class="hint">Every other setting — CPU model, devices, boot order — can be changed later on the machine page or in its XML.</p>
        </div>
      {/if}
    </section>
  </div>

  <aside class="summary ui-panel">
    <h2>Summary</h2>
    <dl>
      <dt>Name</dt><dd>{name || '—'}</dd>
      <dt>System</dt><dd>{OSES.find((o) => o.key === os)?.label}</dd>
      <dt>Source</dt><dd class="mono small">{(source === 'iso' ? iso : source === 'cloud' ? baseImage : existingDisk).split('/').pop() || (source === 'iso' && withDisk ? 'Empty disk' : '—')}</dd>
      <dt>CPU / RAM</dt><dd>{vcpus} vCPU · {memGiB} GiB</dd>
      <dt>Disk</dt><dd>{source === 'disk' ? 'Existing image' : source === 'cloud' ? `${Math.max(diskGiB, baseCapGiB)} GiB copy in ${pool}` : withDisk ? `${diskGiB} GiB in ${pool}` : 'None'}</dd>
      {#if source === 'cloud'}<dt>Sign-in</dt><dd class="mono small">{ciUser}{ciPassword ? ' · password' : ''}{keys.length ? ` · ${keys.length} key${keys.length > 1 ? 's' : ''}` : ''}</dd>{/if}
      <dt>Network</dt><dd>{netChoice.startsWith('network:') ? netChoice.slice(8) : netChoice === 'bridge' ? bridge : 'None'}</dd>
      <dt>Firmware</dt><dd>{firmware === 'uefi' ? 'UEFI' : 'BIOS'}{secureBoot && firmware === 'uefi' ? ', Secure Boot' : ''}{tpm ? ', TPM' : ''}</dd>
    </dl>
    <label class="ui-switch"><input type="checkbox" bind:checked={start} /> Start it right away</label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
    {#if !session.can('admin')}<p class="error">{session.why('admin')}</p>{/if}
    <button class="ui-btn primary create" disabled={!canCreate || busy || !session.can('admin')}>
      <Rocket size={15} />
      {busy ? 'Creating…' : start ? 'Create and start' : 'Create'}
    </button>
    {#if !bootable}<span class="error">Add a disk or installation media.</span>{/if}
  </aside>
</form>

<style>
  .back {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 14px;
    color: var(--text-3);
    font-size: 13px;
    text-decoration: none;
  }
  .back:hover {
    color: var(--text);
  }
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 300px;
    gap: 16px;
    align-items: start;
  }
  .steps {
    display: grid;
    gap: 14px;
  }
  .step {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 16px 18px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 14.5px;
    font-weight: 600;
  }
  .n {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
  }
  .choices {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 8px;
  }
  .choice {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    cursor: pointer;
    transition:
      border-color var(--dur-fast),
      background var(--dur-fast);
  }
  .choice input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .choice.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .choice:focus-within {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .choice strong {
    font-size: 14px;
  }
  .choice span {
    color: var(--text-3);
    font-size: 12px;
  }
  .slider {
    display: grid;
    grid-template-columns: 130px minmax(0, 1fr) 90px;
    align-items: center;
    gap: 6px 14px;
  }
  .slider label {
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
  }
  .slider input[type='range'] {
    width: 100%;
    accent-color: var(--accent);
  }
  .slider .hint {
    grid-column: 2 / -1;
  }
  .num-in {
    height: 32px;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .hint a {
    color: var(--accent);
    text-decoration: none;
  }
  .warn {
    color: var(--warn);
  }
  .error {
    margin: 0;
    color: var(--danger);
    font-size: 12.5px;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .seg {
    display: flex;
    gap: 2px;
    padding: 3px;
    border-radius: 9px;
    background: var(--surface-2);
  }
  .seg label {
    flex: 1;
    display: grid;
    place-items: center;
    height: 32px;
    border-radius: 7px;
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
  }
  .seg input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .seg label.on {
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18);
  }
  .seg label:focus-within {
    outline: 2px solid var(--accent);
  }
  .ci {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 12px;
    border: 1px dashed var(--border-strong);
    border-radius: var(--radius);
  }
  .adv {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 0;
    border: 0;
    background: none;
    font-size: 14.5px;
    font-weight: 600;
    text-align: left;
    cursor: pointer;
  }
  .adv-sum {
    margin-left: auto;
    color: var(--text-3);
    font-size: 12.5px;
    font-weight: 400;
  }
  .chev {
    display: grid;
    color: var(--text-3);
    transition: transform var(--dur) var(--ease-out);
  }
  .chev.up {
    transform: rotate(180deg);
  }
  .adv-body {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .summary {
    position: sticky;
    top: 20px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 16px 18px;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 6px 12px;
    margin: 0;
    font-size: 13px;
  }
  dt {
    color: var(--text-3);
  }
  dd {
    margin: 0;
    text-align: right;
    overflow-wrap: anywhere;
  }
  .small {
    font-size: 11.5px;
  }
  .create {
    height: 40px;
  }
  @media (max-width: 1000px) {
    .layout {
      grid-template-columns: 1fr;
    }
    .summary {
      position: static;
    }
  }
  @media (max-width: 640px) {
    .choices,
    .two {
      grid-template-columns: 1fr;
    }
    .slider {
      grid-template-columns: 1fr 80px;
    }
    .slider label {
      grid-column: 1 / -1;
    }
    .slider .hint {
      grid-column: 1 / -1;
    }
  }
</style>
