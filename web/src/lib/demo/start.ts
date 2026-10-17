// Demo entry: installs the pretend API and shows the demo strip above the app (deep links
// from GitHub Pages' 404 page are restored before any module runs, in index.html).

import { mount } from 'svelte'
import DemoBanner from './DemoBanner.svelte'
import './install'

const strip = document.createElement('div')
document.body.prepend(strip)
mount(DemoBanner, { target: strip })
