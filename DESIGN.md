---
name: PacketTrail
description: The PacketTrail desktop app's own visual world, carried onto its public website.
colors:
  download-blue: "#43afff"
  download-blue-light: "#0a7be8"
  download-ink-light: "#005fc4"
  upload-orange: "#ff9c28"
  upload-orange-light: "#e06a00"
  upload-ink-light: "#b04f00"
  action-blue: "#1d6ad4"
  action-blue-light: "#0a62d0"
  link-blue: "#5aa5ff"
  status-green: "#4aea7d"
  status-green-light: "#00872f"
  demo-violet: "#b99cff"
  demo-violet-light: "#6a43c4"
  ground: "#0c0d10"
  ground-light: "#f2f3f5"
  ground-sunk: "#111216"
  ground-sunk-light: "#e9ebef"
  panel: "#17181c"
  panel-light: "#ffffff"
  ink: "#f2f2f4"
  ink-light: "#1d1d1f"
  ink-muted: "#a8adb7"
  ink-muted-light: "#4f5561"
  ink-faint: "#80858f"
  ink-faint-light: "#6b717c"
  edge: "rgba(255, 255, 255, 0.12)"
  edge-light: "rgba(0, 0, 0, 0.14)"
  hairline: "rgba(255, 255, 255, 0.07)"
  hairline-light: "rgba(0, 0, 0, 0.08)"
  fill: "rgba(118, 118, 128, 0.24)"
  fill-light: "rgba(118, 118, 128, 0.12)"
  fill-strong: "rgba(118, 118, 128, 0.36)"
  fill-strong-light: "rgba(118, 118, 128, 0.2)"
  thumb: "#3a3b42"
  thumb-light: "#ffffff"
typography:
  display:
    fontFamily: "Segoe UI Variable Display, Segoe UI, -apple-system, BlinkMacSystemFont, system-ui, Ubuntu, Cantarell, sans-serif"
    fontSize: "clamp(2.6rem, 5vw, 4.1rem)"
    fontWeight: 700
    lineHeight: 1.08
    letterSpacing: "-0.035em"
  headline:
    fontFamily: "Segoe UI Variable Display, Segoe UI, -apple-system, BlinkMacSystemFont, system-ui, Ubuntu, Cantarell, sans-serif"
    fontSize: "clamp(2rem, 3.6vw, 2.9rem)"
    fontWeight: 700
    lineHeight: 1.08
    letterSpacing: "-0.03em"
  title:
    fontFamily: "Segoe UI Variable Display, Segoe UI, -apple-system, BlinkMacSystemFont, system-ui, Ubuntu, Cantarell, sans-serif"
    fontSize: "1.15rem"
    fontWeight: 650
    lineHeight: 1.08
    letterSpacing: "-0.01em"
  body-large:
    fontFamily: "Segoe UI Variable Text, Segoe UI, -apple-system, BlinkMacSystemFont, system-ui, Ubuntu, Cantarell, Helvetica Neue, Arial, sans-serif"
    fontSize: "1.16rem"
    fontWeight: 400
    lineHeight: 1.6
  body:
    fontFamily: "Segoe UI Variable Text, Segoe UI, -apple-system, BlinkMacSystemFont, system-ui, Ubuntu, Cantarell, Helvetica Neue, Arial, sans-serif"
    fontSize: "17px"
    fontWeight: 400
    lineHeight: 1.6
  label:
    fontFamily: "Segoe UI Variable Text, Segoe UI, -apple-system, BlinkMacSystemFont, system-ui, Ubuntu, Cantarell, Helvetica Neue, Arial, sans-serif"
    fontSize: "0.95rem"
    fontWeight: 600
    lineHeight: 1.2
  data:
    fontFamily: "Segoe UI Variable Display, Segoe UI, -apple-system, BlinkMacSystemFont, system-ui, Ubuntu, Cantarell, sans-serif"
    fontSize: "26px"
    fontWeight: 650
    lineHeight: 1.1
    letterSpacing: "-0.02em"
    fontFeature: "\"tnum\" 1"
  mono:
    fontFamily: "Cascadia Mono, SF Mono, ui-monospace, Consolas, Menlo, monospace"
    fontSize: "0.86em"
rounded:
  code: "5px"
  control: "6px"
  thumb: "7px"
  track: "9px"
  card: "10px"
  frame: "12px"
  pill: "999px"
spacing:
  gutter: "24px"
  gutter-phone: "16px"
  wrap: "1200px"
  card-pad: "22px"
  hero-gap: "48px"
  column-gap: "64px"
  section: "120px"
components:
  button-primary:
    backgroundColor: "{colors.action-blue}"
    textColor: "#ffffff"
    typography: "{typography.label}"
    rounded: "{rounded.control}"
    padding: "11px 19px"
  button-primary-big:
    backgroundColor: "{colors.action-blue}"
    textColor: "#ffffff"
    rounded: "{rounded.control}"
    padding: "14px 24px"
  button-secondary:
    backgroundColor: "{colors.fill}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "11px 19px"
  button-secondary-hover:
    backgroundColor: "{colors.fill-strong}"
  segmented-track:
    backgroundColor: "{colors.fill}"
    rounded: "{rounded.track}"
    padding: "3px"
  segmented-thumb:
    backgroundColor: "{colors.thumb}"
    textColor: "{colors.ink}"
    typography: "{typography.label}"
    rounded: "{rounded.thumb}"
    padding: "8px 20px"
  nav-link:
    textColor: "{colors.ink-muted}"
    rounded: "{rounded.control}"
    padding: "7px 11px"
  nav-link-hover:
    backgroundColor: "{colors.fill}"
    textColor: "{colors.ink}"
  card-panel:
    backgroundColor: "{colors.panel}"
    rounded: "{rounded.frame}"
    padding: "22px 22px 8px"
  satellite-card:
    backgroundColor: "{colors.panel}"
    rounded: "{rounded.card}"
    padding: "13px 16px"
  callout:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.panel}"
    rounded: "{rounded.card}"
    padding: "12px 15px"
  demo-pill:
    textColor: "{colors.demo-violet}"
    rounded: "{rounded.pill}"
    padding: "3px 9px"
  code-inline:
    backgroundColor: "{colors.fill}"
    typography: "{typography.mono}"
    rounded: "{rounded.code}"
    padding: "1px 6px"
---

# Design System: PacketTrail

## Overview

**Creative North Star: "The App, Opened Out"**

The website is not a marketing skin over the product; it is the PacketTrail desktop app's own world laid flat across a page. The same neutral near-black and cool-paper surfaces, the same 1px edges, the same flat GNOME/Adwaita controls and the same two data colours carry over from `internal/web/static/style.css`, mirrored token for token into `docs/site.css`. A visitor who downloads the app should recognise every surface they already saw on the site.

Colour is meaning, not decoration. Download is blue and upload is orange everywhere: in live dots, chart lines, table rates, and the Ghost Fibers ribbons behind the hero. Everything else stays neutral, so the data colours are the only strong hues on screen. Light only ever comes from data marks: a soft halo on live dots and the download line, in dark mode only, never on text.

Density is the app's: tabular numbers, compact tables, small radii, and quiet chrome. The page earns its drama from real data (the animated demo window and cropped screenshots), not from ornament. Both themes are first-class, resolved with `light-dark()` from one token set, and dark is the default ordering (`color-scheme: dark light`).

**Key Characteristics:**
- Neutral surfaces with crisp 1px edges; strong colour reserved for download blue and upload orange.
- Flat controls with small radii (5 to 9px); 10 to 12px for framed content.
- Glow on data marks in dark mode only, driven by one `--glow` strength that is 0% in light mode.
- Segoe UI Variable throughout, as in the app, with tabular numerals for every figure.
- Two registers (Plain and Engineer) that swap in place without layout shift.

## Colors

A neutral, slightly cool grey world in which download blue and upload orange are the only vivid colours, plus a single status green and a demo-only violet.

### Primary
- **Download Blue** (`download-blue`; light theme `download-blue-light` for lines and dots, `download-ink-light` for small text): download traffic. Live dots, the chart's main line and area fill, table download rates, the blue Ghost Fibers ribbons, the logo's packet dot.
- **Upload Orange** (`upload-orange`; light theme `upload-orange-light` for lines, `upload-ink-light` for small text): upload traffic. Live dots, the chart's thinner second line, table upload rates, the orange main Ghost Fibers ribbon, and the 11% warm tint on the demo row that is hogging the line.

### Secondary
- **Action Blue** (`action-blue`; light `action-blue-light`): the fill of the primary button and the skip link. Deliberately deeper than Download Blue so a call to action never reads as a data mark.
- **Link Blue** (`link-blue`; light `action-blue-light`): inline links, focus rings, platform file names, and the highlighted Remote IPs cell in the demo.

### Tertiary
- **Status Green** (`status-green`; light `status-green-light`): the check marks on feature lists. The app's yellow and red status colours belong to the same family but appear on the site only inside the screenshots.
- **Demo Violet** (`demo-violet`; light `demo-violet-light`): only the "Demo data" pill, at 14% tint behind violet text. It marks synthetic data and nothing else.

### Neutral
- **Ground** (`ground` / `ground-light`): the page background, and the browser `theme-color`.
- **Sunk Ground** (`ground-sunk` / `ground-sunk-light`): the full-bleed privacy band and code blocks; one step below the page.
- **Panel** (`panel` / `panel-light`): cards, the demo window, screenshot frames, satellite cards. Also the logo tile in both themes.
- **Ink, Muted Ink, Faint Ink** (`ink`, `ink-muted`, `ink-faint` and their `-light` pairs): headings and emphasis; body copy and secondary labels; fine print, table headers and the footer.
- **Edge** (`edge`): the 1px outline of every card, frame, panel and the header's bottom rule.
- **Hairline** (`hairline`): row separators and the inner rules of the demo window, lighter than Edge.
- **Fill / Fill Strong** (`fill`, `fill-strong`): translucent grey for flat controls at rest and on hover, inline code, and app-icon placeholders.
- **Thumb** (`thumb` / `thumb-light`): the moving thumb of the segmented control.

### Named Rules
**The Two Data Colours Rule.** Blue means download and orange means upload, in every chart, dot, rate and ribbon. Neither is used as a brand fill, a button or a decorative tint.

**The Ink Split Rule.** In light mode each data colour has two values: the vivid line value for strokes, dots and fills (3:1 for graphics), and the darker ink value for small text (4.5:1). Never set text in the line value on a light ground.

## Typography

**Display Font:** Segoe UI Variable Display (with Segoe UI, the platform system UI faces, sans-serif)
**Body Font:** Segoe UI Variable Text (with the same fallbacks plus Helvetica Neue and Arial)
**Label/Mono Font:** Cascadia Mono (with SF Mono, ui-monospace, Consolas, Menlo)

**Character:** The face is the app's own Windows UI face, so the site reads as the product rather than as a campaign. Weight and tight tracking (down to -0.035em at display size) carry the hierarchy; there is no second typeface.

### Hierarchy
- **Display** (700, clamp 2.6 to 4.1rem, 1.08): the single hero H1, held to 14ch so both registers wrap to the same two lines.
- **Headline** (700, clamp 2 to 2.9rem, 1.08): section questions ("Who is it talking to?").
- **Title** (650, 1.15rem): panel headings in the download section.
- **Body Large** (400, 1.16rem, 1.6): the hero lede, in muted ink. Section copy runs at 1.06rem.
- **Body** (400, 17px, 1.6; 16px on phones): all running copy, capped at 64ch with `text-wrap: pretty`.
- **Label** (600, 0.95rem, 1.2): segmented-control and button text; nav links at 0.92rem / 550.
- **Data** (650, 26px, 1.1, tabular numerals): demo-window KPI figures; every number on the page uses tabular numerals.
- **Mono** (0.86em): file names, commands and flags.

### Named Rules
**The Same Two Lines Rule.** Copy that swaps between registers is written to equal length so both versions occupy the same lines. If a register's version wraps longer, rewrite it; do not let the layout grow.

**The Tabular Figures Rule.** Any figure that can change (rates, RTT, loss, counts) is set with `font-variant-numeric: tabular-nums` so values roll without jitter.

## Layout

A 1200px wrap with 24px gutters (16px under 640px). Content sits on two-column grids: the hero is 6/6 with a 48px gap; feature questions and the privacy section are 5/7 and 5/6 with 64px gaps; the download panels are three equal columns at 20px. Sections breathe at 120px vertically, tightening to 40 to 48px where one question follows another.

Product imagery bleeds. The demo window and the bleeding screenshots extend past the wrap to the viewport edge on one side (an `--out` offset computed from the wrap), losing their border and corners on that side. Feature questions alternate which side the image sits on.

At 1080px the section links leave the header. At 960px every grid becomes one column, and left-bleeding shots switch to bleed right. At 640px the demo window fits the column showing whole columns only, satellite cards drop into the flow beneath their screenshot, and screenshots switch to tighter phone crops.

The header is sticky at 64px, translucent with an 18px backdrop blur over the page, the same floating-glass treatment the app reserves for its toolbar.

## Elevation & Depth

Mostly flat, with tonal layering doing most of the work: ground, sunk ground, panel, then translucent fills for controls. Shadows exist only on things that float over content (the demo window, screenshot frames, satellite cards, callouts) and on the segmented thumb's 1px lift. In dark mode the shadows get much deeper so floating frames still separate from a near-black ground.

### Shadow Vocabulary
- **Float** (`box-shadow: light-dark(0 1px 2px rgba(0,0,0,0.08), 0 1px 2px rgba(0,0,0,0.4)), light-dark(0 24px 60px -30px rgba(15,20,30,0.35), 0 30px 80px -30px rgba(0,0,0,0.85))`): the demo window, screenshot frames and satellite cards.
- **Callout** (`box-shadow: 0 14px 34px -14px rgba(0,0,0,0.6)`): the inverted callouts over the demo.
- **Thumb lift** (`box-shadow: 0 1px 2px rgba(0,0,0,0.18), 0 0 0 1px <hairline>`): the segmented control's thumb.
- **Rising row** (`box-shadow: inset 0 -1px <hairline>, 0 6px 16px -8px rgba(0,0,0,0.45)`): a demo row while it climbs the ranking.
- **Data glow** (`0 0 6px` on live dots, `drop-shadow(0 0 3px)` on the download line, mixed at `--glow`): light from data, not depth.

### Named Rules
**The Data-Only Glow Rule.** Glow is a property of data marks: live dots and chart lines. It is mixed through one `--glow` strength (65% dark, 0% light), so it vanishes in light mode by construction. Text, buttons and cards never glow.

**The Flat Control Rule.** Controls are flat fills with no gradient, gloss, bevel or drop shadow; the segmented thumb's 1px lift is the only exception.

## Shapes

Small, consistent corners from the app: 5px on inline code, 6px on buttons, nav links, tabs and focus rings, 7px on the segmented thumb, 9px on its track. Floating notes (callouts and satellite cards) round at 10px; frames for content (demo window, screenshots, panels, the privacy list) at 12px. Pills (the Demo data badge) are fully round. Edges are always 1px, using Edge for outlines and Hairline for internal rules. A frame that bleeds off the viewport drops its border and corners on the bleeding side.

The PacketTrail logo is a routed P-shaped path: a rounded 13-unit Ink stroke links a white source node, an orange intermediate hop and a blue rounded-square packet on a dark tile with a 36/160 corner radius. It represents packets travelling through a network. Keep its geometry identical in the SVG and tools/mkicon renderer; the tile stays dark in both themes.

## Components

### Buttons
Flat and compact, the app's button.
- **Shape:** gently squared (6px).
- **Primary:** Action Blue fill, white 600 text, 11px 19px; a larger 14px 24px variant closes the page. A 16px stroke icon sits 9px before the label.
- **Hover / Focus:** primary lightens by mixing 14% white; secondary steps from Fill to Fill Strong in 120ms. Press scales to 0.97. Focus is a 2px Link Blue ring, offset 3px. Hover styles apply only on fine pointers.
- **Secondary:** translucent Fill with Ink text, same shape and padding.

### Segmented Control (signature)
The Plain | Engineer register switch, taken from the app's segmented control.
- **Style:** a Fill track (9px corners, 3px padding) holding two equal cells; a Thumb-coloured 7px thumb slides between them in 380ms on the house ease. The selected label turns from Muted Ink to Ink.
- **Variants:** full size under the hero headline; a compact size (6px 12px, 0.82rem) in the header. Both drive the same state.
- **Behaviour:** radio semantics with arrow-key movement; the choice persists and can be set from the URL.

### Register Swap (signature)
Every claim exists in both registers, stacked in one grid cell. The outgoing version fades, blurs 4px and drops 4px in 200ms; the incoming version resolves in 320ms. Because both share the cell, nothing below them moves.

### Demo Window (signature)
An animated, faithful miniature of the app on demo data, bleeding off the right edge: a title bar with the logo and a violet "Demo data" pill, Download and Upload KPIs with live dots, a two-line chart (2px blue with glow, 1.6px orange without, blue area at 14%), the app's tab strip, and an Applications table whose rows re-rank with FLIP in 520ms. The row that takes over the line is tinted with 11% upload orange.

### Callouts
Inverted notes over the demo (Ink background, Panel text, 10px corners). A 2px leader line rises from the callout into the row it names and ends in a 9px dot ringed in Panel. Callouts enter by fading, rising 8px and scaling from 0.97 in 360ms.

### Cards / Containers
- **Corner Style:** 12px for panels, frames and the privacy list; 10px for satellite cards.
- **Background:** Panel on Ground; Sunk Ground for the full-bleed privacy band.
- **Shadow Strategy:** Float only on cards that overlap imagery; panels in the flow are flat.
- **Border:** 1px Edge outside, Hairline between list items.
- **Internal Padding:** 22px for panels; 13px 16px for satellite cards; 16px 20px rows in the privacy list.

### Cropped Screenshot with Satellite Card (signature)
Real app screenshots (1600 by 900) are cropped by CSS variables (crop x, y, width and height, plus a separate phone crop) inside a 12px Edge-bordered frame, never re-exported. A satellite card straddles the frame's bottom edge 30px down, 28px in from one side, naming the one detail the crop shows in both registers. On phones the card drops into the flow and the crop tightens, keeping whole the column the card cites.

### Navigation
A sticky 64px bar: the logo and wordmark (Display, 700, 1.1rem), section links in Muted Ink (0.92rem, 550, 6px corners) that gain Ink and a Fill background on hover, the compact register switch, and a GitHub button on Fill. Section links hide below 1080px; the GitHub label hides below 640px, leaving its icon.

### Lists
Feature lists use a drawn 11px by 6px check in Status Green, not a glyph. The privacy list marks each outbound item with a masked 18px arrow icon.

### Ghost Fibers (signature background)
The vendored React Bits WebGL background fills the hero behind everything. Each ribbon is one pure colour (the main ribbon Upload Orange, the others Download Blue), highlights lighten the ribbon's own hue rather than shifting it, and brightness is kept below saturation so the additive blend never washes out to cream. A horizontal mask fades it to about 14% under the copy, and a vertical mask fades its foot. Light mode renders ink-style ribbons using the light-theme line values. Without WebGL it falls back to two soft radial washes of blue and orange.

### Motion
One ease for everything that moves with intent: `cubic-bezier(0.23, 1, 0.32, 1)`. Hover colour changes run at 120ms. Under reduced motion every transition is removed, the demo stops on a poster frame, and smooth scrolling is turned off.

## Do's and Don'ts

### Do:
- **Do** take every surface, edge, radius and colour from the app's tokens in `internal/web/static/style.css`, and keep `docs/site.css` mirroring them.
- **Do** keep download blue and upload orange as the only vivid colours on any screen, and use them only for download and upload.
- **Do** use the darker ink values (`download-ink-light`, `upload-ink-light`) for data-coloured text in light mode.
- **Do** route every glow through the `--glow` strength so light mode has none.
- **Do** outline every card and frame with a 1px Edge, and separate rows with Hairline.
- **Do** write both registers to equal length and stack them in one grid cell so the swap never shifts layout.
- **Do** label synthetic data with the violet "Demo data" pill.
- **Do** crop real screenshots with the crop variables, with a separate phone crop that keeps the cited detail whole.
- **Do** set every changing figure in tabular numerals.

### Don't:
- **Don't** put glow, text-shadow or bloom on text, buttons or cards.
- **Don't** give controls gradients, gloss, bevels or drop shadows; they are flat fills at 6 to 9px.
- **Don't** use Download Blue as a button or link colour; actions use Action Blue and links use Link Blue.
- **Don't** blend two hues inside one Ghost Fibers ribbon; each ribbon is one pure colour.
- **Don't** use Demo Violet for anything except marking demo data.
- **Don't** introduce a second typeface; hierarchy comes from weight, size and tracking.
