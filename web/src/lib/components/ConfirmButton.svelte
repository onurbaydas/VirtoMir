<script lang="ts">
  // Two-click inline confirm for dangerous actions: the first click arms the button (red,
  // with the consequence spelled out), the second runs it. Disarms itself after a few seconds.
  import type { Component } from 'svelte'
  import { errorText } from '../api'
  import { toasts } from '../toast.svelte'

  interface Props {
    label: string
    confirm: string
    run: () => Promise<unknown> | unknown
    icon?: Component<{ size?: number }>
    size?: 'sm' | 'md'
    title?: string
    disabled?: boolean
    /** Toast title when run() throws. */
    failure?: string
  }
  let { label, confirm, run, icon: Icon, size = 'sm', title, disabled = false, failure }: Props = $props()

  let armed = $state(false)
  let busy = $state(false)
  let timer: ReturnType<typeof setTimeout> | undefined

  async function click() {
    if (!armed) {
      armed = true
      clearTimeout(timer)
      timer = setTimeout(() => (armed = false), 4000)
      return
    }
    clearTimeout(timer)
    armed = false
    busy = true
    try {
      await run()
    } catch (e) {
      toasts.push({ kind: 'error', title: failure ?? `${label} failed`, detail: errorText(e) })
    } finally {
      busy = false
    }
  }
</script>

<button
  class="ui-btn {size === 'sm' ? 'sm' : ''} {armed ? 'danger' : 'ghost'}"
  class:busy
  disabled={disabled || busy}
  {title}
  onclick={click}
  onblur={() => (armed = false)}
>
  {#if Icon}<Icon size={size === 'sm' ? 14 : 15} />{/if}
  {busy ? 'Working…' : armed ? confirm : label}
</button>
