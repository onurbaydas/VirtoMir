import { writeFileSync } from 'node:fs'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { defineConfig, type Plugin } from 'vite'

// The Go binary embeds web/dist; the placeholder keeps `go build` working on a fresh clone.
// Vite empties dist/ on every build, so put it back afterwards.
const keepGitkeep: Plugin = {
  name: 'virtomir-keep-gitkeep',
  closeBundle() {
    writeFileSync(new URL('./dist/.gitkeep', import.meta.url), '')
  },
}

// `npm run dev` proxies the API to a running virtomird (default: the Hyper-V dev host).
const api = process.env.VIRTOMIR_API ?? 'https://192.168.77.10:8420'

// `npm run build:demo` (mode "demo", .env.demo): the online demo with a pretend API, built to
// dist-demo/ so it never ends up in the binary. DEMO_BASE is where it is served, e.g.
// /VirtoMir/demo/ on GitHub Pages; `npm run dev:demo` runs it locally without any host.
export default defineConfig(({ mode }) => ({
  base: mode === 'demo' ? (process.env.DEMO_BASE ?? '/') : '/',
  plugins: [svelte(), keepGitkeep],
  server: {
    // secure: false — the dev host serves a self-signed certificate.
    proxy: { '/api': { target: api, changeOrigin: true, secure: false } },
  },
  build: {
    // noVNC uses top-level await.
    target: 'es2022',
    outDir: mode === 'demo' ? 'dist-demo' : 'dist',
    emptyOutDir: true,
  },
}))
