// Parity specimens: the canvas's own content for every Tooltip data-spec
// (CompTooltip, CompTimelineHover, CompTableHover .dc.html), measured against the canvas by `make parity`.
// The surfaces render open and in flow; the canvas positions them absolutely, which the root box ignores.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties } from "react";
import { codexLimitsTip } from "../AgentSectionHead/agentSpecimens";
import { MISSED_COLUMN } from "../CoverageCell/CoverageCell";
import { partialCaptureTip } from "../PartialMark/PartialMark";
import { mergedGapTip, modelBinTip, sessionBandTip } from "../Timeline/timelineTips";
import { readoutLine } from "../Timeline/trackHover";
import { updatedAgoTip } from "../UpdatedAgo/UpdatedAgo";
import { TooltipSurface, unknownValue, type TooltipContent, type TooltipKind } from "./Tooltip";
import { ProvidersTooltips } from "../ProviderMark/CompProviders.canvas.stories";

// `width`: the canvas fixes some boxes' width to show wrapping (staging, not the component).
const P = ({ spec, content, kind, width }: { spec: string; content: TooltipContent; kind?: TooltipKind; width?: number }) => (
  <div data-parity={spec} className="flex items-start">
    <TooltipSurface content={content} kind={kind} style={width ? ({ width } as CSSProperties) : undefined} />
  </div>
);

const binRange = "Nov 3 10:00–10:30";
/** CompR11's snapped session: ended, so its tooltip has no live status. */
const snapSession = {
  sessionId: "4c1a8d2e",
  label: "Fix flaky auth test",
  repo: "web",
  branch: "feat/auth",
  start: new Date(2026, 10, 3, 9, 12).toISOString(),
  end: new Date(2026, 10, 3, 11, 40).toISOString(),
  prompts: 9,
  modelCalls: 214,
  live: false,
} as Parameters<typeof sessionBandTip>[0];
const notRetained = "Content not retained. Only its shape is stored.";

const meta = { title: "Canvas/Overlays/Tooltip", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      {/* CompTooltip */}
      <P
        spec="Tooltip/full"
        content={{
          title: binRange,
          rows: [
            { key: "Model calls", value: "38 · main 31 · subagent 5 · aux 2" },
            { key: "Tokens", value: "1.2M in → 41k out" },
            { key: "Not captured", value: "0" },
          ],
          hint: "Click to select this half hour",
        }}
      />
      <P spec="Tooltip/plain" content={{ body: "Tue Nov 3 2026 · 10:42:16.204 PST" }} />
      <P
        spec="Tooltip/keyValue"
        content={{ rows: [{ key: "Input", value: "43,012" }, { key: "Cache read", value: "40,811" }, { key: "Output", value: "1,207" }] }}
      />
      <P spec="Tooltip/unknownValue" content={{ title: "Cost", rows: [{ key: "costUSD", value: unknownValue("no analytics event for this call") }] }} />
      <P spec="Tooltip/disabledReason" kind="disabled" width={300} content={{ body: "Not available: the request body wasn’t retained, so it can’t be replayed." }} />
      <P
        spec="Tooltip/truncated"
        kind="truncate"
        width={480}
        content={{ body: "/api/oauth/organizations/9c2e41d0-7a1f-4b6e-a0f2-5d8c3e1b77a4/skills/list-skills" }}
      />
      <P spec="Tooltip/columnHeader" width={300} content={{ title: MISSED_COLUMN.header, hint: MISSED_COLUMN.definition }} />
      <P spec="Tooltip/anchoredTop" width={230} content={{ body: notRetained }} />
      <P spec="Tooltip/flippedBottom" width={230} content={{ body: notRetained }} />
      <P spec="Tooltip/chartAnchored" kind="mark" width={200} content={{ body: "Nov 3 10:00–10:30 · 38 calls" }} />

      {/* CompTimelineHover */}
      <P
        spec="Tooltip/barBin"
        kind="mark"
        content={{
          title: binRange,
          rows: [
            { key: "Model calls", value: "38" },
            { key: "", value: "main 31 · subagent 5 · auxiliary 2" },
            { key: "Tokens", value: "1.2M in → 41k out" },
            { key: "Not captured", value: "0" },
          ],
          hint: "Click to select this half hour · drag to select a range",
        }}
      />
      <P
        spec="Tooltip/notCaptured"
        kind="mark"
        content={{
          title: "▲ 57 calls not captured",
          rows: [
            { key: "Window", value: "Nov 3 15:02–15:48 · 46m 0s" },
            { key: "Reported", value: "57 by Claude Code" },
            { key: "Captured", value: "0 by the proxy" },
            { key: "Why", value: "IPv6 route bypassed the proxy" },
          ],
          hint: "Click to list these calls (Requests · not captured)",
        }}
      />
      <P
        spec="Tooltip/densityBin"
        kind="mark"
        content={{
          title: binRange,
          rows: [
            { key: "Events", value: "412" },
            { key: "Top", value: "tengu_tool_progress 210" },
            { key: "", value: "tengu_tool_use_start 88" },
            { key: "", value: "tengu_api_success 41" },
          ],
          hint: "Click to select this half hour",
        }}
      />
      <P
        spec="Tooltip/sessionBand"
        kind="mark"
        content={{
          title: "Added CSV export; tests pass",
          rows: [
            { key: "Repo", value: "web · feat/csv" },
            { key: "Span", value: "Oct 23 17:00 → 20:30 · 3h 30m" },
            { key: "Prompts", value: "8 · 282 calls" },
            { key: "Status", value: "ended" },
          ],
          hint: "Click to open the session",
        }}
      />
      <P
        spec="Tooltip/contextBar"
        kind="mark"
        content={{
          title: "Call 10:42:16 · opus",
          rows: [
            { key: "Context", value: "417.7k tokens · 42% of 1M" },
            { key: "Thread", value: "main" },
            { key: "Prompt", value: "#31" },
          ],
          hint: "Click to open this request",
        }}
      />
      <P
        spec="Tooltip/compaction"
        kind="mark"
        content={{
          title: "Compaction · Nov 3 11:18",
          rows: [
            { key: "Tokens", value: "905k → 21k" },
            { key: "Messages", value: "2,207 summarized" },
            { key: "Wait", value: "1m 11s" },
          ],
          hint: "Click to jump to it in Prompts",
        }}
      />
      <P
        spec="Tooltip/stateSegment"
        kind="mark"
        content={{ title: "Away", rows: [{ key: "Span", value: "Nov 3 12:40 → 14:20 · 1h 40m" }, { key: "Source", value: "focus events · this session" }] }}
      />
      <P
        spec="Tooltip/permissionChanges"
        kind="mark"
        content={{
          title: "Permission changed ×2 in 20 s",
          rows: [
            { key: "15:22:08", value: "bypassPermissions → default" },
            { key: "15:22:28", value: "default → bypassPermissions" },
          ],
          hint: "Click to select these 20 s",
        }}
      />
      <P
        spec="Tooltip/network"
        kind="mark"
        content={{
          title: "net B · has IPv6",
          rows: [
            { key: "Span", value: "Nov 3 15:02 → 18:40 · 3h 38m" },
            { key: "Address", value: "2a01:4b00:…:7c1" },
            { key: "Proxy", value: "▲ bypassed · 57 calls not captured", tone: "danger" },
          ],
          hint: "Click to select this span",
        }}
      />
      <P
        spec="Tooltip/promptMarker"
        kind="mark"
        content={{
          title: "#19 · Nov 3 10:42",
          rows: [
            { key: "Prompt", value: "“fix the flaky auth test”" },
            { key: "Steps", value: "9 · 1m 40s" },
            { key: "Tokens", value: "41k → 3.2k" },
          ],
          hint: "Click to jump to #19 in Prompts",
        }}
      />
      <P
        spec="Tooltip/frictionMarker"
        kind="mark"
        content={{
          title: "▲ Destructive command",
          rows: [
            { key: "When", value: "Nov 3 10:02:37 · #17" },
            { key: "Command", value: "rm -rf (outside cwd) · exit 0" },
            { key: "Tool", value: "Bash" },
          ],
          hint: "Click to jump to #17 in Prompts",
        }}
      />
      <P spec="Tooltip/nowLine" kind="mark" content={{ body: "Now · 11:32:10 · following live" }} />
      <P
        spec="Tooltip/laneLabel"
        content={{ title: "Model calls", hint: "Bars: calls the proxy captured, per bin. Hatched: calls Claude Code reported that the proxy didn’t capture." }}
      />
      <P spec="Tooltip/brushDrag" kind="mark" content={{ body: "Nov 3 10:00 → 12:30 · 2h 30m" }} />
      <P
        spec="Tooltip/barBinFocus"
        kind="mark"
        content={{ title: binRange, rows: [{ key: "Model calls", value: "38" }], hint: "Enter to select · Shift+←/→ to extend" }}
      />

      {/* CompTableHover */}
      <P spec="Tooltip/status" width={280} content={{ rows: [{ key: "Status", value: "200 OK" }, { key: "Stop", value: "tool_use" }] }} />
      <P
        spec="Tooltip/statusError"
        width={300}
        content={{
          title: "Connection reset",
          rows: [
            { key: "After", value: "4.1 s · 1.2 KB received" },
            { key: "Retried", value: "1 · succeeded 10:03:12" },
          ],
        }}
      />
      <P
        spec="Tooltip/duration"
        width={260}
        content={{
          rows: [
            { key: "Total", value: "9,312 ms" },
            { key: "First byte", value: "1,904 ms" },
            { key: "Streaming", value: "7,408 ms" },
          ],
        }}
      />
      <P
        spec="Tooltip/tokens"
        width={260}
        content={{
          rows: [
            { key: "Input", value: "43,012" },
            { key: "Cache read", value: "40,811" },
            { key: "Cache write", value: "1,204 · 5m" },
            { key: "Uncached", value: "997" },
            { key: "Output", value: "1,207" },
          ],
        }}
      />
      <P spec="Tooltip/bytes" width={240} content={{ rows: [{ key: "Up", value: "8,214 B" }, { key: "Down", value: "3,402 B · gzip" }] }} />
      <P spec="Tooltip/time" content={{ body: "Tue Nov 3 2026 · 10:42:16.204 PST" }} />
      <P
        spec="Tooltip/coverage"
        width={300}
        content={{
          title: "94 calls not captured",
          rows: [
            { key: "Captured", value: "640 by the proxy" },
            { key: "Reported", value: "734 by Claude Code" },
            { key: "Why", value: "IPv6 route bypassed the proxy" },
          ],
        }}
      />
      <P
        spec="Tooltip/friction"
        width={300}
        content={{
          rows: [
            { key: "↯ 1", value: "frustrated (user said so)" },
            { key: "✕ 1", value: "interrupted by the user" },
            { key: "▲ 10", value: "destructive commands", tone: "danger" },
          ],
        }}
      />
      <P
        spec="Tooltip/agentChip"
        width={260}
        content={{ title: "Claude Code", rows: [{ key: "Version", value: "2.1.32" }, { key: "First seen", value: "Oct 22 · 1 device" }] }}
      />
      <P spec="Tooltip/unknownCell" width={300} content={{ rows: [{ key: "Repo", value: unknownValue("not parsed before Nov 2") }] }} />
      <P
        spec="Tooltip/columnHeaderCoverage"
        width={300}
        content={{ title: MISSED_COLUMN.header, hint: MISSED_COLUMN.definition }}
      />
      <P
        spec="Tooltip/headerStat"
        content={{
          title: "Model calls",
          rows: [
            { key: "Captured", value: "640 by the proxy" },
            { key: "Not captured", value: "94: reported by Claude Code, no matching request-id", tone: "danger" },
          ],
          hint: "Click +94 to list them",
        }}
      />
      <P
        spec="Tooltip/statTile"
        content={{
          title: "Cost",
          rows: [
            { key: "Source", value: "costUSD from Claude Code analytics" },
            { key: "Estimated", value: "3 calls had no analytics event" },
          ],
        }}
      />
      <P
        spec="Tooltip/coverageLine"
        content={{
          title: "94 calls not captured",
          rows: [
            { key: "Windows", value: "2 · longest Nov 3 15:00–17:59" },
            { key: "Why", value: "IPv6 route bypassed the proxy" },
          ],
          hint: "Click to list them",
        }}
      />
      <P
        spec="Tooltip/keyValueRow"
        content={{ rows: [{ key: "Source", value: "MCP server names in tool definitions" }, { key: "Seen", value: "in 9 of 11 sessions" }] }}
      />
      <P
        spec="Tooltip/notRetained"
        content={{ body: "Content not retained. AIntercept stores its shape (size, counts, types), not the text.", hint: "Click: About this data" }}
      />
      <P spec="Tooltip/postureChip" width={280} content={{ rows: [{ key: "Mode", value: "bypassPermissions" }, { key: "Share", value: "99.7% of calls" }] }} />
      {/* CompDensity (round 10) */}
      <P
        spec="Tooltip/gapMerged"
        content={mergedGapTip([
          { from: new Date(2026, 9, 26, 2, 10).toISOString(), to: new Date(2026, 9, 26, 2, 14, 12).toISOString(), count: 37 },
          { from: new Date(2026, 9, 26, 3, 40).toISOString(), to: new Date(2026, 9, 26, 3, 41, 3).toISOString(), count: 5 },
          { from: new Date(2026, 9, 26, 4, 55).toISOString(), to: new Date(2026, 9, 26, 4, 55, 22).toISOString(), count: 2 },
        ])}
      />
      <P spec="Tooltip/partialCapture" content={partialCaptureTip(7)} />
      {/* CompR11 */}
      <P spec="Tooltip/sessionSnapped" kind="mark" content={sessionBandTip(snapSession)} />
      <P spec="Tooltip/laneReadout" kind="mark" content={readoutLine(new Date(2026, 10, 3, 14, 20).getTime(), "no session open", "nearest: 7a1e… starts 4h 10m later").content} />
      <P spec="Tooltip/emptyBin" kind="mark" content={modelBinTip(new Date(2026, 10, 3, 14).toISOString(), new Date(2026, 10, 3, 15).toISOString(), undefined, 3600)} />
      <P spec="Tooltip/stateReadout" kind="mark" content={readoutLine(new Date(2026, 10, 4, 21, 10).getTime(), "no session open", "last: device asleep until 20:40").content} />
      <P spec="Tooltip/updatedAgo" kind="cell" content={updatedAgoTip("2026-11-08T11:32:05Z", Date.parse("2026-11-08T11:32:17Z"))} />
      <P spec="Tooltip/rangeChip" content={{ body: "Selected range · 2h 30m", hint: "× or Esc clears it" }} />
      <P
        spec="Tooltip/liveBadge"
        width={280}
        content={{
          rows: [
            { key: "Last poll", value: "11:32:10" },
            { key: "Newest data", value: "11:32:06" },
            { key: "Interval", value: "5 s visible · 30 s hidden" },
          ],
        }}
      />
      {/* CompAgents (§11J gap 5). */}
      <P spec="Tooltip/codexLimits" content={codexLimitsTip} />
      {/* CompProviders (§11L). */}
      <ProvidersTooltips />
    </div>
  ),
};
