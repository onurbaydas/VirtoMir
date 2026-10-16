// Archivo (OFL, variable width) for headlines and controls, IBM Plex Mono (OFL) for numbers
// and code; body text uses the system's Arial/Helvetica on purpose (ADR-0038).
import '@fontsource-variable/archivo/standard.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import './app.css'
import { mount } from 'svelte'
import App from './App.svelte'

export default mount(App, { target: document.getElementById('app')! })
