// Archivo (OFL, variable width) for headlines and controls, IBM Plex Mono (OFL) for numbers
// and code; body text uses the system's Arial/Helvetica on purpose (ADR-0038).
import '@fontsource-variable/archivo/standard.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import './app.css'
import { mount } from 'svelte'
import App from './App.svelte'

// The online demo (npm run build:demo) answers the API in the browser; the real build drops
// this branch and the demo code entirely.
if (import.meta.env.VITE_DEMO === '1') await import('./lib/demo/start')

export default mount(App, { target: document.getElementById('app')! })
