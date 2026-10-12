export type ThemePref = 'system' | 'light' | 'dark'

const KEY = 'virtomir.theme'
const media = window.matchMedia('(prefers-color-scheme: dark)')

function load(): ThemePref {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark' || v === 'system') return v
  } catch {
    /* storage blocked */
  }
  return 'system'
}

class Theme {
  pref = $state<ThemePref>(load())
  #systemDark = $state(media.matches)
  resolved = $derived<'light' | 'dark'>(
    this.pref === 'system' ? (this.#systemDark ? 'dark' : 'light') : this.pref,
  )

  constructor() {
    media.addEventListener('change', (e) => (this.#systemDark = e.matches))
    $effect.root(() => {
      $effect(() => {
        document.documentElement.dataset.theme = this.resolved
      })
    })
  }

  set(pref: ThemePref) {
    this.pref = pref
    try {
      localStorage.setItem(KEY, pref)
    } catch {
      /* storage blocked */
    }
  }
}

export const theme = new Theme()
