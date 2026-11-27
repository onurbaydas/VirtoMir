// Regenerates the README's pictures (docs/screenshots/*.png) from the demo build.
//
//   cd web
//   npm i --no-save playwright && npx playwright install chromium   # once
//   npm run screenshots                     # all of them
//   npm run screenshots -- overview hardware # some of them
//
// It starts `vite --mode demo`, opens each page at 1440×900 (the phone at 390×844), hides the
// demo strip and the toasts, waits for the live charts to fill and writes the PNG.
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'

const web = join(dirname(fileURLToPath(import.meta.url)), '..')
const out = join(web, '..', 'docs', 'screenshots')
const PORT = 5199
const base = `http://localhost:${PORT}`
const DB01 = '/machines/4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03' // db-01 in the demo world

const tab = (name) => async (p) => {
  await p.getByRole('tab', { name, exact: true }).or(p.getByRole('button', { name, exact: true })).first().click()
}

// name → page to open, then what to do there before the picture.
const SHOTS = {
  overview: { path: '/' },
  machines: { path: '/machines' },
  'machine-performance': { path: DB01 },
  settings: { path: `${DB01}#settings` },
  snapshots: { path: `${DB01}#snapshots` },
  backups: { path: '/backups' },
  timeline: { path: `${DB01}#timeline` },
  firewall: { path: `${DB01}#firewall` },
  hardware: { path: '/hardware' },
  cluster: { path: '/cluster' },
  ha: { path: '/cluster', then: tab('High availability') },
  pools: { path: '/cluster', then: tab('Resource pools') },
  insights: { path: '/insights' },
  reports: { path: '/reports' },
  'new-machine': { path: '/machines/new' },
  storage: { path: '/storage' },
  networks: { path: '/networks' },
  updates: { path: '/settings' },
  'light-theme': { path: '/', theme: 'light' },
  mobile: { path: '/', phone: true },
}

const pick = process.argv.slice(2)
const unknown = pick.filter((n) => !SHOTS[n])
if (unknown.length) {
  console.error(`unknown: ${unknown.join(', ')}; known: ${Object.keys(SHOTS).join(', ')}`)
  process.exit(2)
}

const vite = spawn(join(web, 'node_modules', '.bin', 'vite'), ['--mode', 'demo', '--port', String(PORT), '--strictPort'], { cwd: web, stdio: ['ignore', 'pipe', 'inherit'] })
await new Promise((resolve, reject) => {
  vite.stdout.on('data', (d) => String(d).includes('ready') && resolve())
  vite.on('exit', (c) => reject(new Error(`vite exited (${c})`)))
})

mkdirSync(out, { recursive: true })
const browser = await chromium.launch()
try {
  for (const [name, s] of Object.entries(SHOTS)) {
    if (pick.length && !pick.includes(name)) continue
    const ctx = await browser.newContext(s.phone ? { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true } : { viewport: { width: 1440, height: 900 } })
    await ctx.addInitScript((theme) => localStorage.setItem('virtomir.theme', theme), s.theme ?? 'dark')
    const p = await ctx.newPage()
    await p.goto(base + s.path)
    await p.waitForTimeout(1500)
    if (s.then) await s.then(p)
    await p.waitForTimeout(4000) // live charts fill from the 1 s samples
    await p.addStyleTag({ content: '.demo-banner, .stack[aria-live] { display: none !important }' })
    await p.waitForTimeout(300)
    await p.screenshot({ path: join(out, `${name}.png`) })
    console.log(`docs/screenshots/${name}.png`)
    await ctx.close()
  }
} finally {
  await browser.close()
  vite.kill()
}
