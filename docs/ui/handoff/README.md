# aintercept UI — handoff bundle

Start here: **AINTERCEPT_UI_HANDOFF.md** is the complete build brief. It covers components, Storybook, screens and the API/data contract, §6A covers interaction and loading behavior, and §6B covers hover, tooltips, linked highlighting and keyboard. Point Claude Code at it.

| Path | What it is |
|---|---|
| `AINTERCEPT_UI_HANDOFF.md` | End-to-end instructions (follow this over the canvas where they differ; see §11) |
| `design/tokens.json` | aintercept Wire design tokens (source) |
| `design/tokens.css` | Generated CSS variables: dark/light themes, type, spacing, radius, motion, plus the proposed `--ch-account`, overlay tokens, `--shadow-pop` and `--link-fill` |
| `design/WIRE_README.md` | The Wire design system's own guidance |
| `canvas/final/` | The final Design canvas: screens, states and component sheets (`Comp*.dc.html`), plus `canvas.json` (layout) and `spec-manifest.json` (every `data-spec` per artboard) |
| `canvas/explorations/` | Earlier exploration canvas (directions A–C and iterations), for reference only |
| `context/CLAUDE_CODE_PERSON_VIEW.md` | Original design brief |
| `context/DATA_ANSWERS.md` | Answers from the captured data (hierarchy, joins, counts) |

About the `.dc.html` files: each is one self-contained artboard. The HTML holds inline styles; the logic is in a `<script type="text/x-dc">` class. They render inside the Design canvas runtime (`support.js`, not included), so treat them as a **visual and markup reference**, not production code. Rebuild them as React components per the handoff.

Canvas file map (final):
- People: `People`, `PeopleLoading`
- Person: `Main` (person page), `PersonRequests`, `PersonLoading`, `PersonWaiting`, `PersonDelayed`, `PersonError`, `PersonEmpty`
- Session: `Session`, `SessionDrawer`, `SessionRequests`, `SessionEvents`, `SessionLoading`, `SessionLive`, `SessionError`, `DrawerNotCaptured`, `DrawerFailed`, `DrawerRetained`
- Components: `CompIndex`, `CompFoundations`, `CompControls`, `CompChips`, `CompSummary`, `CompTimeline`, `CompRows`, `CompDrawer`, `CompStates`, `CompInteraction`, `CompTooltip`, `CompTimelineHover`, `CompTableHover`
