---
version: "alpha"
name: DoneThen Interlock
description: A calm visual system for a cancellable shutdown handoff after Codex stops.
colors:
  canvas: "#0B1110"
  canvas-deep: "#070C0B"
  surface: "#111A18"
  surface-raised: "#17221F"
  surface-soft: "#14201D"
  text: "#F1F6F3"
  text-muted: "#9FB1AA"
  text-faint: "#788B84"
  line: "#2A3A35"
  line-strong: "#3B5049"
  control-line: "#5A766B"
  primary: "#9AD8B4"
  safe-strong: "#BCEBCF"
  signal: "#F0C75E"
  danger: "#EF8B78"
  on-primary: "#08110E"
  code-surface: "#0C1311"
  code-text: "#D7E7DF"
  code-comment: "#D7D0B7"
  print-ink: "#161C1A"
  print-paper: "#FFFFFF"
typography:
  display:
    fontFamily: Space Grotesk
    fontSize: 4.5rem
    fontWeight: 600
    lineHeight: 0.98
    letterSpacing: -0.055em
  heading:
    fontFamily: Space Grotesk
    fontSize: 2.5rem
    fontWeight: 600
    lineHeight: 1.08
    letterSpacing: -0.04em
  body:
    fontFamily: IBM Plex Sans
    fontSize: 1rem
    fontWeight: 400
    lineHeight: 1.7
  utility:
    fontFamily: IBM Plex Mono
    fontSize: 0.75rem
    fontWeight: 500
    lineHeight: 1.4
    letterSpacing: 0.08em
rounded:
  xs: 2px
  sm: 6px
  md: 12px
  lg: 20px
  pill: 999px
spacing:
  xs: 4px
  sm: 8px
  md: 16px
  lg: 24px
  xl: 40px
  section: 112px
components:
  page:
    backgroundColor: "{colors.canvas}"
    textColor: "{colors.text}"
  body-muted:
    textColor: "{colors.text-muted}"
  body-faint:
    textColor: "{colors.text-faint}"
  print-page:
    backgroundColor: "{colors.print-paper}"
    textColor: "{colors.print-ink}"
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
    typography: "{typography.utility}"
    rounded: "{rounded.pill}"
    padding: 12px 18px
  button-primary-hover:
    backgroundColor: "{colors.safe-strong}"
  button-secondary:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.text}"
    typography: "{typography.utility}"
    rounded: "{rounded.pill}"
    padding: 12px 18px
  interlock-panel:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    rounded: "{rounded.lg}"
    padding: 24px
  interlock-nested:
    backgroundColor: "{colors.canvas-deep}"
    textColor: "{colors.text}"
    rounded: "{rounded.md}"
  quiet-panel:
    backgroundColor: "{colors.surface-soft}"
    textColor: "{colors.text-muted}"
    rounded: "{rounded.md}"
  command-surface:
    backgroundColor: "{colors.code-surface}"
    textColor: "{colors.code-text}"
    rounded: "{rounded.lg}"
  code-comment:
    textColor: "{colors.code-comment}"
  utility-caption:
    textColor: "{colors.text-muted}"
    typography: "{typography.utility}"
  divider:
    backgroundColor: "{colors.line}"
  divider-strong:
    backgroundColor: "{colors.line-strong}"
  control-boundary:
    backgroundColor: "{colors.control-line}"
  status-lamp:
    backgroundColor: "{colors.primary}"
    rounded: "{rounded.xs}"
  status-hold:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.signal}"
    typography: "{typography.utility}"
    rounded: "{rounded.sm}"
  status-blocked:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.danger}"
    typography: "{typography.utility}"
    rounded: "{rounded.sm}"
---

## Overview

DoneThen should feel like a trustworthy timer and interlock panel, not a generic
AI landing page and not a theatrical hacker terminal. The visual language comes
from safety checklists, process traces, and calm industrial instrumentation:
dark low-glare surfaces, readable labels, one eucalyptus action color, and a
small amber warning channel.

The page's signature is the handoff from an armed Codex turn to a cancellable
countdown. It must make the product outcome clear at a glance while keeping the
central caveat equally visible: `Stop` is a lifecycle event, not proof of task
success. Everything around that demonstration stays quiet and precise.

## Colors

- **Canvas** is a green-black rather than pure black, reducing glare without
  drifting into neon cyberpunk.
- **Text** is a soft mineral white. Muted copy remains comfortably readable.
- **Safe** communicates an armed or cancellable path and is the only dominant
  accent. It must not imply semantic task success.
- **Signal** is reserved for observe-only or waiting states.
- **Danger** appears only for failed or blocked evidence, never as decoration.
- Decorative gradients are not part of the system. Depth comes from thin
  borders, surface steps, and a restrained technical grid.

## Typography

Space Grotesk carries the product thesis and section headings. IBM Plex Sans
handles explanatory copy. IBM Plex Mono is reserved for lifecycle events,
status labels, commands, and identifiers. Monospace is semantic here: it means
machine-observed state, not "developer aesthetic" decoration.

Headlines use tight tracking and compact line height. Body copy stays open and
plain. All-caps is limited to short utility labels with generous tracking.

## Layout

Every top-level section uses the same 1180px container and horizontal padding.
The hero is a split composition: thesis and actions on the left, the evidence
chain on the right. Subsequent sections alternate between explanatory copy and
structured lifecycle artifacts rather than repeating a grid of generic cards.

Desktop sections have generous vertical intervals. Mobile collapses to one
column without hiding product truth or replacing the evidence chain with an
image. Documentation pages use a narrow reading column with a sticky local
navigation rail on wide screens.

## Elevation & Depth

Surfaces differ by one tonal step and a 1px line. Shadows are used only on the
sticky header and the interlock panel; there are no floating glass cards.
Focus and hover states brighten the border or safe color without changing
layout.

## Shapes

Panels use 12–20px radii; controls use pill radii because they are compact
state selectors. Lifecycle rails, rules, and squared status lamps counter the
softness. Avoid nested rounded rectangles unless each boundary represents a
real system boundary.

## Components

- **Wordmark:** text-only `DoneThen`, with a small pre-alpha status label.
- **Authority chain:** ordered arm, bind, Stop observation, final-authority
  gate, and future recovery states.
- **Scenario selector:** dry-run Stop, continued conversation, and a rejected
  execute request, with the fail-closed result shown explicitly.
- **Command surface:** a real PowerShell snippet with copy support.
- **Status strip:** short factual statements such as `OBSERVE-ONLY` and
  `FAIL-CLOSED`; never vanity statistics.
- **Documentation shell:** the same header, tokens, link treatment, code blocks,
  callouts, and reading rhythm as the landing page.

## Do's and Don'ts

Do:

- Lead with the user outcome—shut down after Codex stops—then immediately
  explain why a stopped turn is not proof of successful completion.
- Keep the pre-alpha capability and current `execute_ready=false` boundary
  visible in the hero and interactive result.
- Use sequence numbers only for genuine lifecycle order.
- Respect keyboard focus, reduced motion, and narrow viewports.
- Prefer precise product language over claims such as "revolutionary" or
  "effortless".

Don't:

- Use purple gradients, glowing orbs, emoji feature icons, or fake dashboards.
- Present pending real-host acceptance as completed platform support.
- Describe `Stop`, `SessionEnd`, or elapsed time as successful completion.
- Scatter animation across every element; the evidence trace is the one
  orchestrated moment.
- Copy another project's logo, marketing copy, screenshots, or brand assets.
