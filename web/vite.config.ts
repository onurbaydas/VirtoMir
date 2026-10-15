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

export default defineConfig({
  plugins: [svelte(), keepGitkeep],
  server: {
    // secure: false — the dev host serves a self-signed certificate.
    proxy: { '/api': { target: api, changeOrigin: true, secure: false } },
  },
  build: {
    // noVNC uses top-level await.
    target: 'es2022',
    outDir: 'dist',
    emptyOutDir: true,
  },
})
