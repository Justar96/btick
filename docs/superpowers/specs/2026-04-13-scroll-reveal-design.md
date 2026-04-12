# Scroll-to-Reveal Effect — Index Page

## Overview

Add a scroll-driven zoom + burst effect to the index page hero. As the user scrolls, the 3D coin scales up while spinning, then rapidly expands past viewport edges and vanishes, revealing a feature highlights section behind it.

## Scroll Zone Structure

```
<div class="scrollZone">          <!-- 200vh, creates scroll room -->
  <div class="stickyFrame">       <!-- position: sticky, pins to viewport center -->
    <div class="heroContent">     <!-- title + tagline + CTAs — fades out first -->
    <div class="coinFrame">       <!-- transform target: scale() + opacity -->
      <BitcoinCoin3D />           <!-- Three.js canvas, keeps spinning via useFrame -->
    </div>
  </div>
</div>
<section class="features">        <!-- normal document flow, scrolls in after burst -->
  2 highlight cards
</section>
```

The scroll zone is 200vh tall. The sticky frame pins the hero content to the viewport center while the user scrolls through the zone. A scroll event listener calculates progress (0–1) through the zone and applies transforms via DOM refs — zero React state updates during scroll.

## Scroll Timeline

The 3D coin continues spinning throughout all phases (Three.js `useFrame` is unaffected by CSS transforms on the container).

| Progress | Coin container | Hero text / CTAs |
|----------|---------------|------------------|
| 0–0.3 | Scale 1×, full opacity | Opacity 1 → 0 |
| 0.3–0.65 | Scale 1× → 3× | Hidden (opacity 0) |
| 0.65–0.8 | Scale 3× → 15×, opacity 1 → 0 (burst) | Hidden |
| 0.8+ | Gone (opacity 0, scale irrelevant) | Hidden |

### Easing

- Text fade: linear
- Coin scale (0.3–0.65): ease-out — starts fast, slows approaching 3×
- Burst scale (0.65–0.8): ease-in — accelerates into the burst, feels like it's flying toward you
- Burst opacity: linear fade to 0

### Performance

- `will-change: transform, opacity` on coin frame and hero content
- All transforms applied via `ref.current.style.transform` — no `setState` during scroll
- Scroll listener uses passive event listener
- `requestAnimationFrame` not needed — transforms are cheap composited properties

## Feature Section

Two product highlight cards in a single horizontal row, centered. Each card has a custom inline SVG illustration above a title and one-line description.

### Card 1: Multi-venue median

**Illustration:** 4 thin lines in exchange brand colors (Binance #f0b90b, Coinbase #0052ff, Kraken #5741d9, OKX #1a1a1a at 50% opacity) starting spread apart on the left and converging to a single point on the right, where a bold black dot marks the median. A dashed vertical line marks the convergence zone with a "median" label.

**Title:** Multi-venue median
**Description:** 4 exchanges, 1 canonical price

### Card 2: Sub-second delivery

**Illustration:** Horizontal timeline with evenly-spaced tick marks at 1-second intervals. Labels in monospace: t-4, t-3, t-2, t-1, now. The "now" tick has concentric pulse rings radiating outward (3 rings at decreasing opacity). A "1s" interval marker between two ticks.

**Title:** Sub-second delivery
**Description:** 1s snapshots, live WebSocket stream

### Card Styling

- Thin `1px solid var(--border)` border (#ebebeb)
- Border radius: 12px
- Background: var(--bg) (#fafafa)
- Illustration area: ~140px height, centered SVG
- Title: 15px, font-weight 600, var(--text)
- Description: 13px, var(--text-muted)
- Max card width: ~300px each
- Gap between cards: 24px
- Section has a small centered label above: "Why btick" in uppercase, letter-spaced, var(--text-muted)

## Files Changed

| File | Change |
|------|--------|
| `web/src/routes/index.tsx` | Add scroll zone wrapper, scroll event handler with refs, feature section with SVG cards |
| `web/src/routes/index.module.css` | Scroll zone (200vh), sticky frame, coin frame, hero content fade, feature section layout, card styles, responsive breakpoint |

## No New Dependencies

Pure scroll listener + CSS transforms + inline SVG. No framer-motion, no GSAP, no intersection observer libraries.

## Responsive

- Below 768px: feature cards stack vertically (single column)
- Coin burst effect works the same — CSS `scale()` overflow is clipped by viewport naturally
- Scroll zone height could reduce to 180vh on mobile for shorter scroll distance

## Existing Component Changes

**BitcoinCoin3D** — No changes. The component already renders inside a container div. The scroll effect applies CSS transforms to the *parent* container (`.coinFrame`), not to the 3D component itself. The Three.js canvas continues its `useFrame` animation loop regardless of CSS transforms on ancestor elements.

**Index page layout** — The current `.page` class with `min-height: calc(100vh - 50px)` and centered flexbox becomes the sticky frame inside the scroll zone. The features section is new content appended after the scroll zone.
