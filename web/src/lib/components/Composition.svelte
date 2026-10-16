<script lang="ts">
  // An abstract-expressionist panel (ADR-0038): stacked colour fields with torn, brushed
  // edges (after Rothko) crossed by black gestural strokes and drips (after Kline and
  // Pollock). Pure SVG — the roughness comes from turbulence displacement filters.
  let { seed = 3 }: { seed?: number } = $props()
  const id = `c${Math.random().toString(36).slice(2, 8)}`
</script>

<svg class="composition" viewBox="0 0 600 800" preserveAspectRatio="xMidYMid slice" aria-hidden="true">
  <defs>
    <filter id="{id}-edge" x="-10%" y="-10%" width="120%" height="120%">
      <feTurbulence type="fractalNoise" baseFrequency="0.018 0.09" numOctaves="4" {seed} />
      <feDisplacementMap in="SourceGraphic" scale="34" />
    </filter>
    <filter id="{id}-brush" x="-10%" y="-30%" width="120%" height="160%">
      <feTurbulence type="fractalNoise" baseFrequency="0.05 0.5" numOctaves="3" seed={seed + 4} />
      <feDisplacementMap in="SourceGraphic" scale="22" />
    </filter>
    <filter id="{id}-grain">
      <feTurbulence type="fractalNoise" baseFrequency="0.9" numOctaves="2" seed={seed + 9} />
      <feColorMatrix values="0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 0.16 0" />
      <feComposite in2="SourceGraphic" operator="in" />
    </filter>
  </defs>

  <rect width="600" height="800" fill="var(--ink)" />
  <!-- Colour fields, stacked like a Rothko -->
  <g filter="url(#{id}-edge)">
    <rect x="48" y="52" width="504" height="250" fill="var(--cadmium)" />
    <rect x="62" y="330" width="478" height="120" fill="var(--ochre)" />
    <rect x="44" y="476" width="512" height="270" fill="var(--cobalt)" />
  </g>
  <!-- Gestural black strokes, after Kline -->
  <g filter="url(#{id}-brush)" stroke="#0b0b0b" fill="none" stroke-linecap="square">
    <path d="M120 120 L 470 640" stroke-width="46" />
    <path d="M60 560 C 220 520, 380 600, 560 500" stroke-width="30" />
    <path d="M430 90 L 380 360" stroke-width="22" />
  </g>
  <!-- Drips -->
  <g fill="#0b0b0b">
    <rect x="300" y="430" width="6" height="120" />
    <rect x="318" y="438" width="4" height="70" />
    <rect x="452" y="600" width="5" height="110" />
  </g>
  <!-- A thin paper scratch -->
  <path d="M90 700 C 220 690, 330 720, 520 690" stroke="#f4f1e8" stroke-width="3" fill="none" filter="url(#{id}-brush)" />
  <rect width="600" height="800" fill="#fff" filter="url(#{id}-grain)" />
</svg>

<style>
  .composition {
    display: block;
    width: 100%;
    height: 100%;
  }
</style>
