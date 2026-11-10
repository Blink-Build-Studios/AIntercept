Wire is the instrument direction for the aintercept person view: dark, dense, monospaced, closer to Burp Suite and a browser's network panel than to a dashboard. The "so much here" reaction comes from density and alignment — thousands of rows that line up — not from decoration.

## Stance

- **Answers to the brief's open questions.** Dark by default (light theme provided). Scale ribbon is one quiet mono line, not a banner. "All requests" is a top-level tab with equal billing — it is what an engineer reaches for first. Detail opens in a right-hand side panel so the list stays visible. Tokens show all four kinds; output tokens and cost lead.
- **Columns, not cards.** Every row is a fixed-column mono line (`data`, 12.5/20). Sessions, prompts, steps and raw requests share the same column rhythm so the eye can drop levels without re-reading.
- **Colour is a channel, never decoration.** Use `ch-model`, `ch-analytics`, `ch-flags`, `ch-skills`, `ch-count` only to say which channel something came from. Everything else is `ink`, `ink-muted`, `ink-faint`.

## Page anatomy

Build the person view top to bottom from these components; the letters are the brief's sections.

| Region | Component | Notes |
|---|---|---|
| A | `PersonHeader` | Window presets and Live/Pause live here. |
| B | `ActivityStrip` | Brushing filters everything below. |
| C, D | `ScaleRibbon`, `CoverageCheck` | Coverage is one quiet line unless there is a gap. |
| E, F | `Capabilities`, `Footprint` | Side by side at ≥1280px, stacked below. |
| G, I | `SessionRow` | Tabs above: Sessions · All requests (`RequestLog`). |
| H | `SessionPanel` | Opens inside an expanded session. |
| J | `StepRow` | Under an open prompt. |
| K | `DetailPanel` | Right side panel, ~40% width; the list keeps its scroll. |
| — | `Controls`, `Chips`, `States` | Shared parts. |

Agent identity: `agent-claude-code` is live; `agent-codex`, `agent-chatgpt`, `agent-claude-ai` are reserved so later agents slot into the header and session bands without new design.

## Content fundamentals

- Neutral voice; never "your". "Claude Code reported 304 model calls; the proxy captured 267."
- Numbers exact, tabular figures. Abbreviate above 100k only (`140.9M`). Units lowercase after the number: `37.2 MB`, `4.8s`.
- Unknown is `—` with a tooltip, never `0`. Content not kept: its shape (`188 chars`) plus a `not retained` chip.
- Never run labels and values together in one same-weight line. A key is `ink-faint` (in headers: uppercase 10.5px above its value); its value is `ink` at 500; groups are split by a `line` hairline, not by spacing alone. Middots separate parts of one value (`macOS 15.1 · iTerm2`), never different facts.
- Field names appear verbatim in mono when they help (`bypassPermissions`, `tool_use`); prose stays plain.

## Visual foundations

- **Ground:** `bg` page, `surface` panels and rows, `surface-raised` for hover/selected/open. `line` hairlines between rows; `line-strong` for control borders.
- **Type:** IBM Plex Mono for all data (`data`, `data-strong`, `num-xl`, `micro`); IBM Plex Sans for the person name (`title`), sentences (`body`) and controls (`label`).
- **Spacing:** 4px base. Rows pad `space-1` vertically, `space-3` horizontally. Panels gap `space-2`.
- **Corners:** square (`radius-0`) for rows, lanes, panels; `radius-sm` for chips and buttons; `radius-pill` only for the live dot.
- **Elevation:** none. Separation is hairlines and the `surface`/`surface-raised` step.
- **States:** hover `surface-raised`; selected `surface-raised` plus `ink` text; focus a 2px solid `focus` ring. Background calls 55% opacity.
- **Live:** `live` green is reserved for the live dot and live chip. New rows append without moving what is open; an "N new" pill when scrolled.

## Iconography

No icon set. Status glyphs are text in the mono face, always beside a word: `▾ ▸` disclosure, `↯` frustrated, `✕` interrupted, `▲` destructive, `⤷` subagent, `●` live. No emoji.
