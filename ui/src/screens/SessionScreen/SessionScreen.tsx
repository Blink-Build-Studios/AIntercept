import { AboutThisData, type AboutState } from "../../components/AboutThisData/AboutThisData";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { Schemas, SessionTimeline } from "../../api/types";
import { formatCount, formatDuration, formatPct, formatTokens } from "../../lib/format";
import { slowNotice, type Region } from "../../lib/region";
import { useLatest } from "../../lib/useLatest";
import { formatDayTime, formatTime } from "../../lib/time";
import { AppBar } from "../../components/AppBar/AppBar";
import type { Agent } from "../../components/AgentChip/AgentChip";
import { Banner, type BannerProps } from "../../components/Banner/Banner";
import { Button } from "../../components/Button/Button";
import { CopyButton } from "../../components/CopyButton/CopyButton";
import { DataTable } from "../../components/DataTable/DataTable";
import { EmptyState } from "../../components/EmptyState/EmptyState";
import { ErrorState } from "../../components/ErrorState/ErrorState";
import { KeyValueList, type KeyValueRow } from "../../components/KeyValueList/KeyValueList";
import type { LiveBadgeProps } from "../../components/LiveBadge/LiveBadge";
import { NewRowsPill } from "../../components/NewRowsPill/NewRowsPill";
import { sessionFacts, threadOrigin, type Capability } from "../../lib/agents";
import { HeaderStat } from "../../components/HeaderStat/HeaderStat";
import { HeaderStatGroup } from "../../components/HeaderStatGroup/HeaderStatGroup";
import { OutcomeSlot } from "../../components/OutcomeSlot/OutcomeSlot";
import { SessionHeader } from "../../components/SessionHeader/SessionHeader";
import { Pagination } from "../../components/Pagination/Pagination";
import { ReplyPreview } from "../../components/TurnRow/ReplyPreview";
import { SubagentRow } from "../../components/TurnRow/SubagentRow";
import { TURN_COLUMNS, TURN_MIN_WIDTH, TurnRow } from "../../components/TurnRow/TurnRow";
import { TurnStep } from "../../components/TurnRow/TurnStep";
import { WorkGroup } from "../../components/TurnRow/WorkGroup";
import { datedRows, outcomeFacts, turnView, type TurnView } from "../../lib/turns";
import { RangeReadout } from "../../components/RangeReadout/RangeReadout";
import { RegionState } from "../../components/RegionState/RegionState";
import { RequestDrawer, type RequestDrawerProps } from "../../components/RequestDrawer/RequestDrawer";
import { RequestRow, type RequestKind } from "../../components/RequestRow/RequestRow";
import { RequestFilters } from "../../components/RequestFilters/RequestFilters";
import { RequestTable } from "../../components/RequestTable/RequestTable";
import { SessionTabs } from "../../components/SessionTabs/SessionTabs";
import { SESSION_TIMELINE_LANES, Skeleton, SkeletonBar } from "../../components/Skeleton/Skeleton";
import { TextInput } from "../../components/TextInput/TextInput";
import { BarTrack } from "../../components/Timeline/BarTrack";
import type { Range } from "../../components/Timeline/Brush";
import { laneInteractions, sessionInteractions } from "../../components/Timeline/interactions";
import { FrictionTrack, frictionRowLabels } from "../../components/Timeline/FrictionTrack";
import { Lane, LANE_HEIGHT } from "../../components/Timeline/Lane";
import { TurnTrack } from "../../components/Timeline/TurnTrack";
import { WorkTrack } from "../../components/Timeline/WorkTrack";
import { sessionLanes } from "../../components/Timeline/sessionLanes";
import { Timeline } from "../../components/Timeline/Timeline";
import { LANE_DEFINITIONS, nowTip } from "../../components/Timeline/timelineTips";
import { requestChannels, requestDetail, sessionTabs, sessionTitle, tabHint } from "./sessionView";
import "./SessionScreen.css";

export type SessionTab = "turns" | "requests";
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
  /** Range-scoped (`from`/`to`): `/state`, `/turns`, `/requests`. */
  state: Region<Schemas["SessionState"]>;
  turns: Region<Schemas["SessionTurnsResponse"]>;
  requests: Region<Schemas["SessionRequestsResponse"]>;
  onRetry: (region: "session" | "timeline" | "state" | "turns" | "requests") => void;
  /** A session page (a Codex subagent's own thread, `open thread ›`). */
  sessionHref?: (sessionId: string) => string;

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
  /** The Requests tab's filters. `hidden`: include hidden calls (Codex's prewarm and title calls; the API leaves them out unless asked). */
  requestFilter: { q: string; kinds: RequestKind[]; hidden?: boolean };
  onRequestFilter: (f: { q: string; kinds: RequestKind[]; hidden?: boolean }) => void;
  /** A prompt's hidden-calls note: Requests with hidden calls shown, as one URL change. */
  onShowHiddenRequests?: () => void;

  /** Opens the drawer on a request (the row's data shows at once). */
  onOpenRequest: (requestId: string, seed?: Schemas["RequestRow"]) => void;
  /** The open drawer, from `?req` (its Regions from the page). */
  drawer?: Omit<RequestDrawerProps, "onClose"> | null;
  onCloseDrawer: () => void;
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
}: {
  data: SessionTimeline;
  selection: Range | null;
  following: boolean;
  onSelect: (r: Range | null) => void;
  onOpenRequest: (requestId: string) => void;
  onJumpToPrompt: (index: number, t?: string) => void;
}) {
  // The lanes are thousands of marks: built once per timeline payload, so a page
  // re-render (a poll's fetching flag, a tab) or the brush moving doesn't redo them.
  const l = useMemo(() => sessionLanes(data), [data]);
  const cb = useLatest({ onSelect, onOpenRequest, onJumpToPrompt });
  const lanes = useMemo(() => {
    const actions = {
      select: (r: Range) => cb.current.onSelect(r),
      openRequest: (id: string) => cb.current.onOpenRequest(id),
      jumpToPrompt: (index: number, t?: string) => cb.current.onJumpToPrompt(index, t),
    };
    const on = laneInteractions(data, actions);
    const own = sessionInteractions(data, actions);
    return (
      <>
        <Lane label="Turns" height={26} summary={`${l.turns.length} turns`}>
          <TurnTrack blocks={l.turns} now={data.now ?? data.to} interact={own.turns} />
        </Lane>
        <Lane label="Work" sublabel={l.perBin} height={LANE_HEIGHT.workSession} summary={`${l.work.length} active bins`} definition={LANE_DEFINITIONS.work}>
          <WorkTrack bins={l.work} binSeconds={l.binSeconds} interact={on.work} />
        </Lane>
        <Lane label="Context" sublabel="main thread" height={56} summary={`peak ${l.context.peak} tokens; ${l.context.labels.length} compactions`}>
          <BarTrack bins={l.context.bins} binSeconds={l.binSeconds} color="count" level labels={l.context.labels} interact={own.context} labelTip={own.compactionTip} />
        </Lane>
        <Lane label="Friction" height={LANE_HEIGHT.friction} summary={`${l.friction.length} marks`} rows={frictionRowLabels(l.friction)} subRows>
          <FrictionTrack marks={l.friction} interact={on.friction} />
        </Lane>
      </>
    );
  }, [data, l, cb]);
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

/**
 * The state panels (handoff §7.3, §11S), within the range: the repo when content was recorded.
 * Capabilities, Recaps and Experiments came from the agent's analytics and are gone.
 */
function StatePanels({ st }: { st: Schemas["SessionState"] }) {
  const r = st.repoInRange;
  const repo: KeyValueRow[] = [
    { k: "Branch", v: r.branch, unknownReason: "not in the conversation" },
    { k: "Head", v: r.headFrom && r.headTo ? `${short(r.headFrom)} → ${short(r.headTo)}` : null, unknownReason: "no head change seen" },
    { k: "Commits", v: formatCount(r.commits) },
    { k: "Push", v: r.pushed ? "pushed" : "no push in range" },
    ...(r.prs.length ? [{ k: "PRs", v: r.prs.map((n) => `#${n}`).join(", ") }] : []),
  ];
  return (
    <div className="session-panels">
      <KeyValueList title="Repo · in range" rows={repo} footer={r.cwdTrail?.length ? `cwd ${r.cwdTrail.join(" → ")}` : undefined} />
    </div>
  );
}

/**
 * The session header (handoff §11L.5): the ProviderMark (`CODEX · SESSION · EXEC 0.164.0`), the
 * title and ids, then only the facts and stats this agent reports (P2): no placeholder for
 * the rest (§11S: nothing from the agents' analytics, no cost). Codex: workspace · sha, cwd,
 * origin, reasoning, hidden calls, limits at the end.
 */
function SessionHeaderBlock({ s, now }: { s: Schemas["SessionHeader"]; now: string }) {
  const has = (f: Capability) => s.capabilities.includes(f);
  const { title, sub } = sessionTitle(s);
  const facts = sessionFacts(s).filter((f) => has(f.k as Capability));
  const origin = has("origin") ? threadOrigin(s) : null;
  const client = [s.agent === "codex" ? s.client : null, s.clientVersion].filter(Boolean).join(" ") || null;
  const hidden = has("hiddenCalls") && s.hiddenCalls ? s.hiddenCalls : 0;
  const edited = s.outcome.editedFiles ?? s.work.editedFiles ?? (s.work.edits || null);
  const lines = s.work.linesAdded !== null && s.work.linesRemoved !== null ? `+${formatCount(s.work.linesAdded)} −${formatCount(s.work.linesRemoved)}` : undefined;
  const limits = has("limits") && s.limitsAtEnd?.windows.length ? s.limitsAtEnd.windows.map((w) => `${w.name} ${w.usedPct === null ? "—" : `${Math.round(w.usedPct)}%`}`).join(" · ") : null;
  return (
    <SessionHeader
      title={title}
      agent={s.agent}
      client={client}
      sub={sub}
      note={null}
      facts={facts}
      origin={origin}
      stats={
        <HeaderStatGroup label="Session" wrapGap>
          <HeaderStat
            label="Span"
            value={`${formatDayTime(s.start)} → ${s.end ? formatTime(s.end, new Date(now)) : "now"} · ${formatDuration(Date.parse(s.end ?? now) - Date.parse(s.start))}`}
            sub={`${formatDuration(s.activeMs)} active`}
          />
          <HeaderStat label="Turns" value={formatCount(s.turns)} sub={s.turnsInterrupted ? `${formatCount(s.turnsInterrupted)} interrupted` : undefined} />
          <HeaderStat label="Steps" value={formatCount(s.modelCalls)} qualifier={hidden ? `+${formatCount(hidden)} hidden` : undefined} qualifierTone="hidden" sub="model calls" />
          <HeaderStat label="Tokens" value={sessionTokens(s.tokens)} sub={cacheShare(s.tokens)} unknownReason="no usage on these calls" />
          {edited !== null && <HeaderStat label="Edited" value={`${formatCount(edited)} ${edited === 1 ? "file" : "files"}`} sub={lines} />}
          <HeaderStat label="Commits" value={formatCount(s.outcome.commits)} sub={s.outcome.headFrom && s.outcome.headTo && s.outcome.headFrom !== s.outcome.headTo ? `${s.outcome.headFrom.slice(0, 4)} → ${s.outcome.headTo.slice(0, 4)}` : undefined} />
          {has("reasoning") && <HeaderStat label="Reasoning" value={s.effort} unknownReason="no effort on these calls" />}
          {limits && <HeaderStat label="Limits at end" value={limits} />}
        </HeaderStatGroup>
      }
    />
  );
}

/**
 * The Turns tab's rows (handoff §11R, Session.dc.html `DataTable/turns`): a TurnRow per turn;
 * open, its WorkGroups (each opening to its TurnSteps, a reply to its preview) and its
 * subagents. A step opens the drawer on its call.
 */
function TurnRows({
  turns,
  expanded,
  onToggle,
  groups,
  onToggleGroup,
  arrived,
  selected,
  onOpenStep,
  sessionHref,
  now,
}: {
  turns: TurnView[];
  expanded: string[];
  onToggle: (id: string) => void;
  groups: string[];
  onToggleGroup: (key: string) => void;
  arrived: string | null;
  selected: string | null;
  onOpenStep: (requestId: string) => void;
  sessionHref?: (sessionId: string) => string;
  now: string;
}) {
  const dated = datedRows(turns.map((v) => v.turn.startedAt));
  return (
    <>
      {turns.map((v, i) => (
        <TurnRow key={v.turn.promptId} turn={v.turn} work={v.work} dated={dated[i]} expanded={expanded.includes(v.turn.promptId)} arrived={arrived === v.turn.promptId} onToggle={onToggle} now={now}>
          {v.groups.map((g) => (
            <WorkGroup key={g.key} kind={g.kind} title={g.title} meta={g.meta} exits={g.exits} expanded={groups.includes(g.key)} onToggle={() => onToggleGroup(g.key)}>
              {g.reply && <ReplyPreview text={g.reply.text} size={g.reply.size} />}
              {g.steps.map((st) => (
                <TurnStep key={st.requestId} label={st.label} detail={st.detail} selected={st.requestId === selected} onOpen={() => onOpenStep(st.requestId)} />
              ))}
            </WorkGroup>
          ))}
          {v.subagents.map((a) => (
            <SubagentRow
              key={a.key}
              name={a.name}
              n={a.n}
              work={a.work}
              tokens={a.tokens}
              href={a.sessionId && sessionHref ? sessionHref(a.sessionId) : undefined}
              onOpen={a.requestId ? () => onOpenStep(a.requestId as string) : undefined}
            />
          ))}
        </TurnRow>
      ))}
    </>
  );
}

/** `92% cache read`: the share of input tokens read from the cache; null when unknown. */
function cacheShare(t: { input: number | null; cacheRead: number | null; cacheWrite: number | null }): string | null {
  const sent = (t.input ?? 0) + (t.cacheRead ?? 0) + (t.cacheWrite ?? 0);
  return t.cacheRead === null || sent === 0 ? null : `${formatPct(Math.round((t.cacheRead / sent) * 100))} cache read`;
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
 * whole-session timeline (the range control), the range readout, the state
 * panels and the Turns / Requests tabs, all scoped to the range; the request
 * drawer over it. Pure: every value comes in as props.
 */
export function SessionScreen(p: SessionScreenProps) {
  const s = p.session.data;
  const now = p.now ?? new Date().toISOString();
  const [expanded, setExpanded] = useState<string[]>([]);
  const [groups, setGroups] = useState<string[]>([]);
  // Prewarm and title calls are left out of Requests unless asked for (§11J gap 2): the API
  // counts them (`hiddenCount`) and sends them with `hidden=true`.
  const showHidden = !!p.requestFilter.hidden;
  const setShowHidden = (hidden: boolean) => p.onRequestFilter({ ...p.requestFilter, hidden });
  const hiddenCount = p.requests.data?.hiddenCount ?? p.session.data?.hiddenCalls ?? 0;
  const following = p.rangeMode === "following";
  const listLength = (p.tab === "turns" ? p.turns.data?.data.length : p.requests.data?.data.length) ?? 0;
  const pill = useNewRows(listLength, following);
  const turns = useMemo(() => (p.turns.data?.data ?? []).map(turnView), [p.turns.data]);
  // In-flight turns start open (§6.5).
  useEffect(() => {
    const open = p.turns.data?.data.flatMap((t) => (t.live ? [t.promptId] : [])) ?? [];
    if (open.length) setExpanded((e) => [...new Set([...e, ...open])]);
  }, [p.turns.data]);
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
    const hit = p.turns.data?.data.find((t) => t.n === j.index);
    if (!hit) return;
    jumped.current = j.seq;
    setExpanded((e) => (e.includes(hit.promptId) ? e : [...e, hit.promptId]));
    setArrived(hit.promptId);
    requestAnimationFrame(() => {
      const row = document.querySelector<HTMLElement>(`[data-turn-index="${j.index}"]`);
      row?.scrollIntoView?.({ block: "center" }); // optional: jsdom (tests) has none
      row?.focus({ preventScroll: true, focusVisible: keyboard.current } as FocusOptions);
    });
  }, [p.jumpTo, p.turns.data]);
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

  return (
    <div className="session-screen">
      {appBar}
      {p.banner && <Banner {...p.banner} page />}
      <div className="session-head">
      {/* Header (§11L.5): the ProviderMark, the title, then only the fields this agent reports. */}
      {s ? <div className="session-head-box"><SessionHeaderBlock s={s} now={now} /></div> : null}
      {/* Where a generated summary will go, and the facts we capture (§11R). */}
      {s && <OutcomeSlot title="Outcome" facts={outcomeFacts(s.outcome, { branch: s.branch })} />}
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
            <Skeleton shape="timeline" lanes={SESSION_TIMELINE_LANES} />,
            p.timeline.data && (
              <SessionTimelinePanel
                data={p.timeline.data}
                selection={p.rangeMode === "whole" ? null : p.range}
                following={following}
                onSelect={p.onRange}
                onOpenRequest={(id) => p.onOpenRequest(id)}
                onJumpToPrompt={(index, t) => (p.onJumpToPrompt ? p.onJumpToPrompt(index, t) : p.onTab("turns"))}
              />
            ),
          )}
          <RangeReadout
            turns
            mode={following ? "live" : "range"}
            range={p.range}
            counts={p.state.status === "ready" && counts ? { ...counts, prompts: counts.turns, modelCalls: counts.steps } : null}
            onPrev={p.onPrevRange}
            onNext={p.onNextRange}
            onClear={() => p.onRange(null)}
          />
        </section>

        {/* State panels */}
        {region(p.state, "state", "the session state", <PanelsSkeleton />, p.state.data && <StatePanels st={p.state.data} />)}

        {/* Tabs */}
        <section aria-label="Session activity" className="session-tabs">
          <div className="session-tabbar">
            <SessionTabs label="Session activity" tabs={tabs} value={p.tab} onChange={(t) => p.onTab(t as SessionTab)} hint={hint} />
            <span className="flex-1" />
            <span className="type-data text-ink-faint">{rangeText}</span>
          </div>

          {p.tab === "turns" &&
            region(
              p.turns,
              "turns",
              "turns",
              <DataTable expandable columns={TURN_COLUMNS} minWidth={TURN_MIN_WIDTH} label="Turns" loading />,
              p.turns.data && (
                <DataTable expandable columns={TURN_COLUMNS} minWidth={TURN_MIN_WIDTH} label="Turns" footer={pagination(p.turns.data.meta, p.turns.status === "refetching")}>
                  <TurnRows
                    turns={turns}
                    expanded={expanded}
                    onToggle={(id) => setExpanded((e) => (e.includes(id) ? e.filter((x) => x !== id) : [...e, id]))}
                    groups={groups}
                    onToggleGroup={(id) => setGroups((e) => (e.includes(id) ? e.filter((x) => x !== id) : [...e, id]))}
                    arrived={arrived}
                    selected={selected}
                    onOpenStep={(id) => p.onOpenRequest(id)}
                    sessionHref={p.sessionHref}
                    now={now}
                  />
                </DataTable>
              ),
              <EmptyState compact title="No turns in this range" actions={<Button onClick={() => p.onRange(null)}>Whole session</Button>} />,
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
                <EmptyState compact title="No requests match" actions={<Button onClick={() => p.onRequestFilter({ q: "", kinds: [], hidden: p.requestFilter.hidden })}>Clear filters</Button>} />,
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
