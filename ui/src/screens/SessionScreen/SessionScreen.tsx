import { AboutThisData, type AboutState } from "../../components/AboutThisData/AboutThisData";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { Schemas, SessionTimeline } from "../../api/types";
import { formatCount, formatDuration, formatPct, formatTokens } from "../../lib/format";
import { slowNotice, type Region } from "../../lib/region";
import { useLatest } from "../../lib/useLatest";
import { formatDayTime, formatTime } from "../../lib/time";
import { modelCallsTip } from "../../lib/tips";
import { AppBar } from "../../components/AppBar/AppBar";
import { AGENT_NAME, type Agent } from "../../components/AgentChip/AgentChip";
import { Banner, type BannerProps } from "../../components/Banner/Banner";
import { BetweenRow } from "../../components/BetweenRow/BetweenRow";
import { Button } from "../../components/Button/Button";
import { CopyButton } from "../../components/CopyButton/CopyButton";
import { DataTable } from "../../components/DataTable/DataTable";
import { EmptyState } from "../../components/EmptyState/EmptyState";
import { ErrorState } from "../../components/ErrorState/ErrorState";
import { EVENT_COLUMNS, EVENT_MIN_WIDTH, EventRow, type EventLink } from "../../components/EventRow/EventRow";
import { KeyValueList, type KeyValueRow } from "../../components/KeyValueList/KeyValueList";
import type { LiveBadgeProps } from "../../components/LiveBadge/LiveBadge";
import { NewRowsPill } from "../../components/NewRowsPill/NewRowsPill";
import { sessionFacts, threadOrigin, type Capability } from "../../lib/agents";
import { HeaderStat } from "../../components/HeaderStat/HeaderStat";
import { HeaderStatGroup } from "../../components/HeaderStatGroup/HeaderStatGroup";
import { PartialMark } from "../../components/PartialMark/PartialMark";
import { SessionHeader } from "../../components/SessionHeader/SessionHeader";
import { Pagination } from "../../components/Pagination/Pagination";
import { PROMPT_COLUMNS, PROMPT_MIN_WIDTH, PromptRow } from "../../components/PromptRow/PromptRow";
import { RangeReadout } from "../../components/RangeReadout/RangeReadout";
import { RegionState } from "../../components/RegionState/RegionState";
import { RequestDrawer, type RequestDrawerProps } from "../../components/RequestDrawer/RequestDrawer";
import { RequestRow, type RequestKind } from "../../components/RequestRow/RequestRow";
import { RequestFilters } from "../../components/RequestFilters/RequestFilters";
import { RequestTable } from "../../components/RequestTable/RequestTable";
import { SessionTabs } from "../../components/SessionTabs/SessionTabs";
import { SkeletonBar } from "../../components/Skeleton/Skeleton";
import { TextInput } from "../../components/TextInput/TextInput";
import { ToggleChip } from "../../components/ToggleChip/ToggleChip";
import { FilterChip } from "../../components/FilterChip/FilterChip";
import { BarTrack, gapRowLabel } from "../../components/Timeline/BarTrack";
import type { Range } from "../../components/Timeline/Brush";
import { laneInteractions, sessionInteractions } from "../../components/Timeline/interactions";
import { FrictionTrack, frictionRowLabels } from "../../components/Timeline/FrictionTrack";
import { Lane, LANE_HEIGHT } from "../../components/Timeline/Lane";
import { MarkerTrack } from "../../components/Timeline/MarkerTrack";
import { RowTrack } from "../../components/Timeline/RowTrack";
import { sessionLanes } from "../../components/Timeline/sessionLanes";
import { StateTrack } from "../../components/Timeline/StateTrack";
import { Timeline } from "../../components/Timeline/Timeline";
import { LANE_DEFINITIONS, nowTip } from "../../components/Timeline/timelineTips";
import { requestChannels, requestDetail, sessionTabs, sessionTitle, tabHint } from "./sessionView";
import "./SessionScreen.css";

export type SessionTab = "prompts" | "requests" | "events";
export const PAGE_SIZE = 20;

export type SessionScreenProps = {
  /** "About this data": the AppBar button and the drawer (`?about=1`). */
  about?: AboutState;
  sessionId: string;
  /** `/sessions/:id`: the header; its error with no data is the page's load failure. */
  session: Region<Schemas["SessionHeader"]>;
  /** The person, for the breadcrumb ("name unknown" until extraction). */
  /** The session's person, for the breadcrumb: name, page and agents (the crumb's dots). */
  person?: { name: string | null; href: string; agents?: Agent[] } | null;
  /** `/sessions/:id/timeline`: the whole session, fetched once and brushed here. */
  timeline: Region<SessionTimeline>;
  /** Range-scoped (`from`/`to`): `/state`, `/prompts`, `/requests`, `/events`. */
  state: Region<Schemas["SessionState"]>;
  prompts: Region<Schemas["SessionPromptsResponse"]>;
  requests: Region<Schemas["SessionRequestsResponse"]>;
  events: Region<Schemas["SessionEventsResponse"]>;
  onRetry: (region: "session" | "timeline" | "state" | "prompts" | "requests" | "events") => void;

  live: LiveBadgeProps;
  paused: boolean;
  onPause: () => void;
  /** Delayed (no data for 3 min while live) or a failed refresh (data kept, backing off). */
  banner?: BannerProps | null;
  /** The link to this view, for Copy link. */
  link: string;

  /**
   * The brush: `following` while live and the viewer hasn't brushed (the last
   * 15 min, ending now); `range` when brushed; `whole` otherwise (an ended session).
   */
  rangeMode: "following" | "range" | "whole";
  range: Range;
  onRange: (r: Range | null) => void;
  onPrevRange: () => void;
  onNextRange: () => void;

  tab: SessionTab;
  onTab: (t: SessionTab) => void;
  /** The prompt a timeline click asked for: opened and scrolled to once its page arrives. */
  jumpTo?: { index: number; seq: number } | null;
  onJumpToPrompt?: (index: number, t?: string) => void;
  page: number;
  onPage: (p: number) => void;
  /** The Requests tab's filters; `notCaptured` = only calls the proxy didn't capture (`captured=false`). */
  /** `hidden`: include hidden calls (Codex's prewarm and title calls; the API leaves them out unless asked). */
  requestFilter: { q: string; kinds: RequestKind[]; notCaptured: boolean; hidden?: boolean };
  onRequestFilter: (f: { q: string; kinds: RequestKind[]; notCaptured: boolean; hidden?: boolean }) => void;
  /** A prompt's hidden-calls note: Requests with hidden calls shown, as one URL change. */
  onShowHiddenRequests?: () => void;
  eventFilter: { q: string; internal: boolean; ticks: boolean };
  onEventFilter: (f: { q: string; internal: boolean; ticks: boolean }) => void;

  /** Opens the drawer on a request (the row's data shows at once). */
  onOpenRequest: (requestId: string, seed?: Schemas["RequestRow"]) => void;
  /** The open drawer, from `?req` (its Regions from the page). */
  drawer?: Omit<RequestDrawerProps, "onClose"> | null;
  onCloseDrawer: () => void;
  /** Expanded events' full JSON (`/events/:id`), by event id; toggling one fetches it. */
  eventJson: Record<string, Region<Schemas["EventDetail"]>>;
  onToggleEvent: (eventId: string) => void;
  onRetryEvent: (eventId: string) => void;
  /** The reference now (stories pin it). */
  now?: string;
};

/** A short id: "4c1a…77e0". */
const short = (id: string) => (id.length > 12 ? `${id.slice(0, 4)}…${id.slice(-4)}` : id);

/**
 * The whole-session timeline (handoff §7.3), fetched once: its brush is the
 * page's range. Live and not brushed, the brush follows the last 15 min.
 */
function SessionTimelinePanel({
  data,
  selection,
  following,
  onSelect,
  onOpenRequest,
  onJumpToPrompt,
  onListNotCaptured,
  capabilities,
}: {
  data: SessionTimeline;
  /** The agent's own fields (§11L): a lane it doesn't report isn't drawn; unknown (loading) draws them all. */
  capabilities?: readonly Capability[];
  selection: Range | null;
  following: boolean;
  onSelect: (r: Range | null) => void;
  onOpenRequest: (requestId: string) => void;
  onJumpToPrompt: (index: number, t?: string) => void;
  onListNotCaptured: () => void;
}) {
  // The lanes are thousands of marks: built once per timeline payload, so a page
  // re-render (a poll's fetching flag, a tab) or the brush moving doesn't redo them.
  const l = useMemo(() => sessionLanes(data), [data]);
  const has = (f: Capability) => !capabilities || capabilities.includes(f);
  const permission = has("permission");
  const focus = has("focus") || has("presence");
  const device = has("device");
  const cb = useLatest({ onSelect, onOpenRequest, onJumpToPrompt, onListNotCaptured });
  const lanes = useMemo(() => {
    const actions = {
      select: (r: Range) => cb.current.onSelect(r),
      openRequest: (id: string) => cb.current.onOpenRequest(id),
      jumpToPrompt: (index: number, t?: string) => cb.current.onJumpToPrompt(index, t),
      listNotCaptured: () => cb.current.onListNotCaptured(),
    };
    const on = laneInteractions(data, actions);
    const own = sessionInteractions(data, actions);
    return (
      <>
        <Lane label="Prompts" height={26} summary={`${l.prompts.markers.length} prompts`}>
          <MarkerTrack markers={l.prompts.markers} labels={l.prompts.labels} interact={on.prompts} />
        </Lane>
        <Lane
          label="Model calls"
          sublabel={l.perBin}
          height={LANE_HEIGHT.barsWithGapRow}
          summary={`${l.modelCalls.bins.length} active bins; ${l.modelCalls.gaps.length} gaps`}
          definition={LANE_DEFINITIONS.modelCalls}
          footer={gapRowLabel(l.modelCalls.gaps)}
        >
          <BarTrack bins={l.modelCalls.bins} binSeconds={l.binSeconds} color="model" gaps={l.modelCalls.gaps} gapRow interact={on.modelCalls} gapTip={on.gapTip} onGap={on.onGap} />
        </Lane>
        <Lane label="Context" sublabel="main thread" height={56} summary={`peak ${l.context.peak} tokens; ${l.context.labels.length} compactions`}>
          <BarTrack bins={l.context.bins} binSeconds={l.binSeconds} color="count" level labels={l.context.labels} interact={own.context} labelTip={own.compactionTip} />
        </Lane>
        {permission && <Lane label="Permission" height={30} summary={`${l.permission.segments.length} modes; ${l.permission.changes.length} brief changes`}>
          <StateTrack segments={l.permission.segments} changes={l.permission.changes} palette="permission" nowrap interact={own.permission} />
        </Lane>}
        {focus && <Lane label="Focus" height={LANE_HEIGHT.rows(2)} summary="this terminal: focused and blurred" rows={l.focusRows.map((r) => r.name)}>
          <RowTrack rows={l.focusRows} interact={on.presence} />
        </Lane>}
        {device && <Lane label="Device" height={LANE_HEIGHT.rows(1)} summary={`${l.sleepRows[0]?.segments.length ?? 0} sleeps`} rows={l.sleepRows.map((r) => r.name)}>
          <RowTrack rows={l.sleepRows} interact={on.presence} />
        </Lane>}
        <Lane label="Network" height={LANE_HEIGHT.single} summary={`${l.network.length} network segments`}>
          <StateTrack segments={l.network} palette="network" nowrap interact={own.network} />
        </Lane>
        <Lane label="Friction" height={LANE_HEIGHT.rows(3)} summary={`${l.friction.length} marks`} rows={frictionRowLabels(l.friction)}>
          <FrictionTrack marks={l.friction} interact={on.friction} />
        </Lane>
      </>
    );
  }, [data, l, cb, permission, focus, device]);
  const header = (
    <>
      <span className="tl-key">Session timeline</span>
      <span className="font-mono text-ink-muted">whole session · {l.binsLabel}</span>
      <span className="flex-1" />
      <span className="font-mono text-ink-faint">drag to select a range · everything below follows it</span>
    </>
  );
  return (
      <Timeline
        {...l.window}
        label="Session timeline"
        header={header}
        selection={selection}
        onSelect={onSelect}
        brushMode={following ? "live" : "range"}
        nowTip={data.now ? nowTip(data.now, true) : undefined}
      >
        {lanes}
      </Timeline>
  );
}

/** The four state panels (handoff §7.3): capabilities, repo, recaps, experiments, at the range's end or within it. */
function StatePanels({ st, at }: { st: Schemas["SessionState"]; at: string }) {
  const c = st.capabilitiesAt;
  const tally = (t: Schemas["ToolTally"]) => (t.used === null ? null : t.total === null ? `${formatCount(t.used)} used` : `${formatCount(t.used)} of ${formatCount(t.total)} used`);
  const caps: KeyValueRow[] = [
    { k: "Built-in tools", v: tally(c.builtin), unknownReason: "not extracted yet" },
    { k: "MCP servers", d: c.mcpServers.map((s) => s.name).join(", ") || undefined, v: formatCount(c.mcpServers.length) },
    { k: "MCP tools", v: tally(c.mcpTools), unknownReason: "not extracted yet" },
    { k: "Skills", v: c.skills === null ? null : formatCount(c.skills), unknownReason: "not extracted yet" },
    { k: "Hooks", v: c.hooks === null ? null : formatCount(c.hooks), unknownReason: "not extracted yet" },
  ];
  const r = st.repoInRange;
  const repo: KeyValueRow[] = [
    { k: "Branch", v: r.branch, unknownReason: "not extracted yet" },
    { k: "Head", v: r.headFrom && r.headTo ? `${short(r.headFrom)} → ${short(r.headTo)}` : null, unknownReason: "no head change seen" },
    { k: "Commits", v: formatCount(r.commits) },
    { k: "Push", v: r.pushed ? "pushed" : "no push in range" },
    ...(r.prs.length ? [{ k: "PRs", v: r.prs.map((n) => `#${n}`).join(", ") }] : []),
  ];
  const recaps = st.recapsInRange ?? [];
  const latest = [...recaps].reverse().find((x) => x.text) ?? recaps[recaps.length - 1];
  const recapRows: KeyValueRow[] = latest
    ? [{ k: formatTime(latest.t, new Date(at)), v: latest.text, unknownReason: "recap text not retained" }]
    : [{ k: "Recaps", v: "none in range" }];
  const ex = st.experimentsAt;
  const exRows: KeyValueRow[] = ex === null ? [{ k: "Experiments", v: null, unknownReason: "not extracted yet" }] : ex.length ? ex.map((e) => ({ k: e.key, v: e.variation })) : [{ k: "Experiments", v: "none" }];
  const atLabel = formatDayTime(at);
  return (
    <div className="session-panels">
      <KeyValueList
        title={`Capabilities · at ${atLabel}`}
        accent="skills"
        rows={caps}
        footer={c.poolChangesInRange ? `${formatCount(c.poolChangesInRange)} tool-pool changes in range` : undefined}
      />
      <KeyValueList title="Repo · in range" rows={repo} footer={r.cwdTrail?.length ? `cwd ${r.cwdTrail.join(" → ")}` : undefined} />
      <KeyValueList
        title={`Recaps · in range${st.recapsTotal === null ? "" : ` · ${formatCount(recaps.length)} of ${formatCount(st.recapsTotal)}`}`}
        rows={recapRows}
      />
      <KeyValueList
        title={`Experiments · at ${atLabel}`}
        accent="flags"
        rows={exRows}
        footer={
          st.flagFetchesInRange === null ? undefined : `${formatCount(st.flagFetchesInRange)} flag fetches in range · ${formatCount(st.flagsChangedInRange ?? 0)} changed`
        }
      />
    </div>
  );
}

/**
 * The session header (handoff §11L.5): the ProviderMark (`CODEX · SESSION · EXEC 0.164.0`), the
 * title and ids, then only the facts and stats this agent reports (P2): no placeholder for
 * the rest. Claude Code: repo · branch as the title, its recap, events, device, approvals
 * bypassed. Codex: workspace · sha, cwd, shell, tz, approval, sandbox, origin, reasoning,
 * hidden calls, limits at the end.
 */
function SessionHeaderBlock({ s, now, onNotCaptured }: { s: Schemas["SessionHeader"]; now: string; onNotCaptured: () => void }) {
  const has = (f: Capability) => s.capabilities.includes(f);
  const { title, sub } = sessionTitle(s);
  const facts = sessionFacts(s).filter((f) => has(f.k as Capability));
  const origin = has("origin") ? threadOrigin(s) : null;
  const client = [s.agent === "codex" ? s.client : null, s.clientVersion].filter(Boolean).join(" ") || null;
  const device = s.device ? [s.device.platform, s.device.shell, s.networks === null ? null : `${formatCount(s.networks)} ${s.networks === 1 ? "network" : "networks"}`].filter(Boolean).join(" · ") || null : null;
  const hidden = has("hiddenCalls") && s.hiddenCalls ? s.hiddenCalls : 0;
  const limits = has("limits") && s.limitsAtEnd?.windows.length ? s.limitsAtEnd.windows.map((w) => `${w.name} ${w.usedPct === null ? "—" : `${Math.round(w.usedPct)}%`}`).join(" · ") : null;
  return (
    <SessionHeader
      title={title}
      agent={s.agent}
      client={client}
      sub={sub}
      note={has("recap") && s.label.source === "recap" ? `Latest recap: “${s.label.text}”` : null}
      facts={facts}
      origin={origin}
      stats={
        <HeaderStatGroup label="Session" wrapGap>
          <HeaderStat label="Span" value={`${formatDayTime(s.start)} → ${s.end ? formatTime(s.end, new Date(now)) : "now"} · ${formatDuration(Date.parse(s.end ?? now) - Date.parse(s.start))}`} />
          <HeaderStat
            label="Prompts"
            value={
              s.prompts === null ? null : (
                <>
                  {formatCount(s.prompts)}
                  {s.capturedFromPrompt !== null && <PartialMark from={s.capturedFromPrompt} />}
                </>
              )
            }
            unknownReason="no prompt ids on these calls"
          />
          <HeaderStat
            label="Model calls"
            value={formatCount(s.modelCalls)}
            qualifier={s.notCaptured ? `+${formatCount(s.notCaptured)} not captured` : hidden ? `+${formatCount(hidden)} hidden` : undefined}
            qualifierTone={s.notCaptured ? "gap" : "hidden"}
            onQualifier={s.notCaptured ? onNotCaptured : undefined}
            tip={modelCallsTip(s.modelCalls, s.notCaptured)}
          />
          <HeaderStat label="Tokens" value={sessionTokens(s.tokens)} unknownReason="no usage on these calls" />
          <HeaderStat label="Tool calls" value={s.toolCalls === null ? null : formatCount(s.toolCalls)} unknownReason="no tool uses recorded on these calls" />
          {has("analytics") && <HeaderStat label="Events" value={s.events === null ? null : formatCount(s.events)} unknownReason="no analytics captured" />}
          {(has("device") || device) && <HeaderStat label="Device" value={device} unknownReason="no device reported" />}
          {has("permission") && <HeaderStat label="Approvals bypassed" value={s.bypassPct === null ? null : `${formatPct(s.bypassPct)} of calls`} unknownReason="no permission mode reported" />}
          {has("reasoning") && <HeaderStat label="Reasoning" value={s.effort} unknownReason="no effort on these calls" />}
          {limits && <HeaderStat label="Limits at end" value={limits} />}
        </HeaderStatGroup>
      }
    />
  );
}

/** A session's tokens as in → out (input includes cache reads and writes); null when unknown. */
function sessionTokens(t: { input: number | null; cacheRead: number | null; cacheWrite: number | null; output: number | null }): string | null {
  if (t.output === null) return null;
  const sent = (t.input ?? 0) + (t.cacheRead ?? 0) + (t.cacheWrite ?? 0);
  return `${formatTokens(sent, "total")} → ${formatTokens(t.output, "total")}`;
}

/** The page's own skeleton, at the loaded page's size (Session.dc.html `Skeleton/page`). */
const PanelsSkeleton = () => (
  <div className="session-panels">
    {[0, 1, 2, 3].map((i) => (
      <div key={i} className="session-panel-skeleton border border-line bg-surface" />
    ))}
  </div>
);

/**
 * Keeps an "N new" count while live rows arrive below and the viewer has scrolled
 * up (handoff §5.3, §6A.9): rows are never inserted out of view without saying so.
 */
function useNewRows(count: number, live: boolean) {
  const [fresh, setFresh] = useState(0);
  const seen = useRef(count);
  useEffect(() => {
    const atBottom = () => window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 80;
    if (live && count > seen.current && !atBottom()) setFresh((n) => n + count - seen.current);
    seen.current = count;
  }, [count, live]);
  return {
    fresh,
    show: () => {
      setFresh(0);
      window.scrollTo({ top: document.documentElement.scrollHeight, behavior: "smooth" });
    },
  };
}

/**
 * The session page (handoff §7.3, Session.dc.html): header and stats, the
 * whole-session timeline (the range control), the range readout, the four
 * state panels and the Prompts / Requests / Events tabs, all scoped to the
 * range; the request drawer over it. Pure: every value comes in as props.
 */
export function SessionScreen(p: SessionScreenProps) {
  const s = p.session.data;
  const now = p.now ?? new Date().toISOString();
  const [expanded, setExpanded] = useState<string[]>([]);
  const [subagents, setSubagents] = useState<string[]>([]);
  // Prewarm and title calls are left out of Requests unless asked for (§11J gap 2): the API
  // counts them (`hiddenCount`) and sends them with `hidden=true`.
  const showHidden = !!p.requestFilter.hidden;
  const setShowHidden = (hidden: boolean) => p.onRequestFilter({ ...p.requestFilter, hidden });
  const hiddenCount = p.requests.data?.hiddenCount ?? p.session.data?.hiddenCalls ?? 0;
  const following = p.rangeMode === "following";
  const listLength = (p.tab === "prompts" ? p.prompts.data?.data.length : p.tab === "requests" ? p.requests.data?.data.length : p.events.data?.data.length) ?? 0;
  const pill = useNewRows(listLength, following);
  // In-flight prompts start open (§6.5).
  useEffect(() => {
    const open = p.prompts.data?.data.flatMap((i) => (i.type === "prompt" && i.prompt.inFlight ? [i.prompt.promptId] : [])) ?? [];
    if (open.length) setExpanded((e) => [...new Set([...e, ...open])]);
  }, [p.prompts.data]);
  // A jump from the timeline (§11H.6): once its page is here, that prompt is selected
  // ("arrived"), opened and centred, with focus (its ring only for keyboard arrivals).
  const jumped = useRef(-1);
  const [arrived, setArrived] = useState<string | null>(null);
  const keyboard = useRef(false);
  useEffect(() => {
    const key = () => (keyboard.current = true);
    const pointer = () => (keyboard.current = false);
    window.addEventListener("keydown", key, true);
    window.addEventListener("pointerdown", pointer, true);
    return () => {
      window.removeEventListener("keydown", key, true);
      window.removeEventListener("pointerdown", pointer, true);
    };
  }, []);
  useEffect(() => {
    const j = p.jumpTo;
    if (!j || jumped.current === j.seq) return;
    const hit = p.prompts.data?.data.find((i) => i.type === "prompt" && i.prompt.index === j.index);
    if (!hit || hit.type !== "prompt") return;
    jumped.current = j.seq;
    setExpanded((e) => (e.includes(hit.prompt.promptId) ? e : [...e, hit.prompt.promptId]));
    setArrived(hit.prompt.promptId);
    requestAnimationFrame(() => {
      const row = document.querySelector<HTMLElement>(`[data-prompt-index="${j.index}"]`);
      row?.scrollIntoView?.({ block: "center" }); // optional: jsdom (tests) has none
      row?.focus({ preventScroll: true, focusVisible: keyboard.current } as FocusOptions);
    });
  }, [p.jumpTo, p.prompts.data]);
  // Selected until the next pointer-down, a tab or page change, or Esc.
  useEffect(() => setArrived(null), [p.tab, p.page]);
  useEffect(() => {
    if (!arrived) return undefined;
    const clear = () => setArrived(null);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && clear();
    window.addEventListener("pointerdown", clear, true);
    window.addEventListener("keydown", esc, true);
    return () => {
      window.removeEventListener("pointerdown", clear, true);
      window.removeEventListener("keydown", esc, true);
    };
  }, [arrived]);

  // Every crumb carries the dot(s) of what it points to (§11M 1.4): the person's agents, the session's agent.
  const crumbs = [
    { label: "people", href: "/people" },
    ...(p.person ? [{ label: p.person.name ?? "name unknown", href: p.person.href, agents: p.person.agents?.length ? p.person.agents : s ? [s.agent] : undefined }] : []),
    { label: s ? sessionTitle(s).title : "session", agents: s ? [s.agent] : undefined },
  ];
  const appBar = (
    <>
      <AppBar
        breadcrumb={crumbs}
        live={p.live}
        onPause={p.onPause}
        paused={p.paused}
        loading={p.session.status === "loading"}
        actions={<CopyButton label="Copy link" variant="default" text={() => p.link} />}
        onAbout={p.about?.onOpen}
      />
      {p.about?.open && <AboutThisData contentRetained={p.about.contentRetained} products={p.about.products} onClose={p.about.onClose} />}
    </>
  );

  if (p.session.status === "error" && !s) {
    return (
      <div className="session-screen">
        {appBar}
        <div className="session-failed">
          <ErrorState
            title="Couldn’t load this session"
            body={`The query for session ${short(p.sessionId)} failed: ${p.session.error?.message ?? "unknown error"}. Capture isn’t affected: the proxy keeps recording while this page can’t load.`}
            meta={p.session.error?.queryId ? `query ${p.session.error.queryId}` : undefined}
            onRetry={() => p.onRetry("session")}
            back={p.person ? { label: "← Back to person", href: p.person.href } : { label: "← Back to people", href: "/people" }}
          />
        </div>
      </div>
    );
  }

  const counts = p.state.data?.counts;
  const tabs = sessionTabs(counts);
  const hint = tabHint(p.tab, s, counts, p.requests.data?.kindCounts, p.person?.name ?? null);
  const rangeText = following ? "last 15 min · oldest first · new rows append below" : `${formatTime(p.range.from, new Date(now))} → ${formatTime(p.range.to, new Date(now))} · oldest first`;
  const region = (r: Region<unknown>, name: Parameters<SessionScreenProps["onRetry"]>[0], label: string, skeleton: ReactNode, children: ReactNode, empty?: ReactNode) => (
    <RegionState
      status={r.status} slow={slowNotice(r, label)}
      hasData={r.data !== undefined}
      skeleton={skeleton}
      empty={empty}
      error={r.error && { message: r.error.message, queryId: r.error.queryId, onRetry: () => p.onRetry(name) }}
      label={label}
    >
      {children}
    </RegionState>
  );
  const pagination = (meta: Schemas["Meta"] | undefined, loading: boolean) => {
    const total = meta?.total ?? 0;
    if (total <= PAGE_SIZE) return null;
    const from = (p.page - 1) * PAGE_SIZE + 1;
    return <Pagination inset page={p.page} pageCount={Math.ceil(total / PAGE_SIZE)} from={from} to={Math.min(total, from + PAGE_SIZE - 1)} total={total} loading={loading} onPage={p.onPage} />;
  };
  const selected = p.drawer?.requestId ?? null;
  const onLink = (l: EventLink) => {
    if (l.kind === "request") p.onOpenRequest(l.id);
  };

  return (
    <div className="session-screen">
      {appBar}
      {p.banner && <Banner {...p.banner} page />}
      <div className="session-head">
      {/* Header (§11L.5): the ProviderMark, the title, then only the fields this agent reports. */}
      {s ? <div className="session-head-box"><SessionHeaderBlock s={s} now={now} onNotCaptured={() => p.onRequestFilter({ q: "", kinds: [], notCaptured: true })} /></div> : null}
      {s ? null : (
        <div className="session-header-skeleton" aria-busy>
          <SkeletonBar width={300} height={22} />
          <SkeletonBar width={220} height={14} faint />
        </div>
      )}

      </div>
      <main className="session-main page-enter">
        {/* Timeline + range readout */}
        <section aria-label="Session timeline" className="session-timeline">
          {region(
            p.timeline,
            "timeline",
            "the session timeline",
            <div className="session-timeline-skeleton border border-line bg-surface" />,
            p.timeline.data && (
              <SessionTimelinePanel
                data={p.timeline.data}
                selection={p.rangeMode === "whole" ? null : p.range}
                following={following}
                onSelect={p.onRange}
                onOpenRequest={(id) => p.onOpenRequest(id)}
                onJumpToPrompt={(index, t) => (p.onJumpToPrompt ? p.onJumpToPrompt(index, t) : p.onTab("prompts"))}
                onListNotCaptured={() => p.onTab("requests")}
                capabilities={s?.capabilities}
              />
            ),
          )}
          <RangeReadout
            mode={following ? "live" : "range"}
            range={p.range}
            counts={p.state.status === "ready" ? (counts ?? null) : null}
            onPrev={p.onPrevRange}
            onNext={p.onNextRange}
            onClear={() => p.onRange(null)}
          />
        </section>

        {/* State panels */}
        {region(p.state, "state", "the session state", <PanelsSkeleton />, p.state.data && <StatePanels st={p.state.data} at={p.range.to} />)}

        {/* Tabs */}
        <section aria-label="Session activity" className="session-tabs">
          <div className="session-tabbar">
            <SessionTabs label="Session activity" tabs={tabs} value={p.tab} onChange={(t) => p.onTab(t as SessionTab)} hint={hint} />
            <span className="flex-1" />
            <span className="type-data text-ink-faint">{rangeText}</span>
          </div>

          {p.tab === "prompts" &&
            region(
              p.prompts,
              "prompts",
              "prompts",
              <DataTable expandable columns={PROMPT_COLUMNS} minWidth={PROMPT_MIN_WIDTH} label="Prompts" loading />,
              p.prompts.data && (
                <DataTable expandable columns={PROMPT_COLUMNS} minWidth={PROMPT_MIN_WIDTH} label="Prompts" footer={pagination(p.prompts.data.meta, p.prompts.status === "refetching")}>
                  {p.prompts.data.data.map((i) =>
                    i.type === "prompt" ? (
                      <PromptRow
                        key={i.prompt.promptId}
                        item={i}
                        expanded={expanded.includes(i.prompt.promptId)}
                        arrived={arrived === i.prompt.promptId}
                        onShowHidden={
                          p.onShowHiddenRequests ??
                          (() => {
                            setShowHidden(true);
                            p.onTab("requests");
                          })
                        }
                        onToggle={(id) => setExpanded((e) => (e.includes(id) ? e.filter((x) => x !== id) : [...e, id]))}
                        onOpenStep={(id) => p.onOpenRequest(id)}
                        selectedRequestId={selected}
                        expandedSubagents={subagents}
                        onToggleSubagent={(id) => setSubagents((e) => (e.includes(id) ? e.filter((x) => x !== id) : [...e, id]))}
                        now={now}
                      />
                    ) : (
                      <BetweenRow key={`${i.between.kind}-${i.between.t}`} between={i.between} onOpen={(id) => p.onOpenRequest(id)} selected={i.between.requestId === selected} now={now} />
                    ),
                  )}
                </DataTable>
              ),
              <EmptyState compact title="No prompts in this range" actions={<Button onClick={() => p.onRange(null)}>Whole session</Button>} />,
            )}

          {p.tab === "requests" && (
            <>
              <div className="session-filters">
                <TextInput
                  id="session-requests-filter"
                  label="Filter"
                  placeholder="path, status…"
                  value={p.requestFilter.q}
                  onChange={(q) => p.onRequestFilter({ ...p.requestFilter, q })}
                  pending={p.requests.status === "refetching"}
                />
                {p.requestFilter.notCaptured && (
                  <FilterChip label="▲ not captured" removeLabel="Remove filter: not captured" onRemove={() => p.onRequestFilter({ ...p.requestFilter, notCaptured: false })} />
                )}
                <RequestFilters
                  channels={requestChannels(s, p.requests.data?.kindCounts)}
                  value={p.requestFilter.kinds}
                  onChange={(kinds) => p.onRequestFilter({ ...p.requestFilter, kinds: kinds as RequestKind[] })}
                  hidden={!s || s.capabilities.includes("hiddenCalls") ? { count: hiddenCount, shown: showHidden, onChange: setShowHidden } : null}
                />
              </div>
              {region(
                p.requests,
                "requests",
                "requests",
                <RequestTable scope="session" loading />,
                p.requests.data && (
                  <RequestTable scope="session" footer={pagination(p.requests.data.meta, p.requests.status === "refetching")}>
                    {p.requests.data.data.map((r) => (
                      <RequestRow key={r.requestId} row={r} detail={requestDetail(r)} onOpen={(row) => p.onOpenRequest(row.requestId, row)} selected={r.requestId === selected} now={now} />
                    ))}
                  </RequestTable>
                ),
                p.requestFilter.notCaptured && !p.requestFilter.q && !p.requestFilter.kinds.length ? (
                  <EmptyState
                    chip={<FilterChip label="▲ not captured" />}
                    title="No uncaptured calls in this range"
                    body={`Every model call ${s ? AGENT_NAME[s.agent] : "the agent"} reported in this range reached the proxy.`}
                    actions={<Button onClick={() => p.onRequestFilter({ ...p.requestFilter, notCaptured: false })}>Clear filter</Button>}
                  />
                ) : (
                  <EmptyState compact title="No requests match" actions={<Button onClick={() => p.onRequestFilter({ q: "", kinds: [], notCaptured: false, hidden: p.requestFilter.hidden })}>Clear filters</Button>} />
                ),
              )}
            </>
          )}

          {p.tab === "events" && (
            <>
              <div className="session-filters">
                <TextInput
                  id="session-events-filter"
                  label="Filter"
                  placeholder="event name, field=value…"
                  value={p.eventFilter.q}
                  onChange={(q) => p.onEventFilter({ ...p.eventFilter, q })}
                  pending={p.events.status === "refetching"}
                />
                <ToggleChip label="hide client internals" pressed={!p.eventFilter.internal} onChange={(on) => p.onEventFilter({ ...p.eventFilter, internal: !on })} />
                <ToggleChip label="collapse ticks" pressed={p.eventFilter.ticks} onChange={(on) => p.onEventFilter({ ...p.eventFilter, ticks: on })} />
                <span className="flex-1" />
                {counts && p.events.data && (
                  <span className="type-data text-ink-faint">{`${formatCount(counts.events)} in range · ${formatCount(p.events.data.meta.total ?? p.events.data.data.length)} shown`}</span>
                )}
              </div>
              {region(
                p.events,
                "events",
                "events",
                <DataTable expandable columns={EVENT_COLUMNS} minWidth={EVENT_MIN_WIDTH} label="Events" loading />,
                p.events.data && (
                  <DataTable expandable columns={EVENT_COLUMNS} minWidth={EVENT_MIN_WIDTH} label="Events" footer={pagination(p.events.data.meta, p.events.status === "refetching")}>
                    {p.events.data.data.map((e) => {
                      const j = p.eventJson[e.eventId];
                      return (
                        <EventRow
                          key={e.eventId}
                          event={e}
                          expanded={j !== undefined}
                          onToggle={() => p.onToggleEvent(e.eventId)}
                          json={j?.data?.raw}
                          jsonError={j?.status === "error" ? { message: j.error?.message, onRetry: () => p.onRetryEvent(e.eventId) } : undefined}
                          onLink={onLink}
                          now={now}
                        />
                      );
                    })}
                  </DataTable>
                ),
                <EmptyState compact title="No events match" actions={<Button onClick={() => p.onEventFilter({ q: "", internal: false, ticks: true })}>Clear filters</Button>} />,
              )}
            </>
          )}
          <div className="session-pill">
            <NewRowsPill count={pill.fresh} direction="down" onClick={pill.show} />
          </div>
        </section>
      </main>
      {p.drawer && <RequestDrawer {...p.drawer} onClose={p.onCloseDrawer} />}
    </div>
  );
}
