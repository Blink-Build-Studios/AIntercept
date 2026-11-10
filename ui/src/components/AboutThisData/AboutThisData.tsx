import { useLayoutEffect, useRef, type KeyboardEvent, type ReactNode } from "react";
import { trapTab } from "../../lib/focusTrap";
import { AGENT_BG, AGENT_NAME, type Agent } from "../AgentChip/AgentChip";
import { Button } from "../Button/Button";
import type { Channel } from "../ChannelSwatch/ChannelSwatch";
import { ChannelSwatch } from "../ChannelSwatch/ChannelSwatch";
import { Chip } from "../Chip/Chip";
import "./AboutThisData.css";

/** What a screen needs to offer "About this data": the AppBar button and the drawer. */
export type AboutState = {
  open: boolean;
  contentRetained: boolean | null;
  /** The products this deployment captures (`capabilities`), once known. */
  products?: AboutProductData[] | null;
  onOpen: () => void;
  onClose: () => void;
};

export type AboutThisDataProps = {
  /** The deployment's retention mode (/status `contentRetained`: true is full interception, bodies recorded); null until it's known. */
  contentRetained: boolean | null;
  /** One per product in `capabilities` (§11M 3.2); null or absent until the overview loads. */
  products?: AboutProductData[] | null;
  onClose: () => void;
};

/** A captured product (§11M 3.2): its agent, token accuracy and sessions in the window. */
export type AboutProductData = { agent: Agent; tokenAccuracy: "exact" | "estimated" | "missing"; sessions: number };

const NODES = {
  agent: ["Agent", "the person’s machine · nothing installed"],
  proxy: ["Envoy proxy", "TLS intercept · customer network"],
  vendor: ["api.anthropic.com", "the vendor"],
  ui: ["AIntercept UI", "this page · polls every 5 s"],
  observer: ["Observer", "parses rows · writes NDJSON to object storage"],
} as const;

/** Each product's channels (§11M 3.2: one ChannelTable per captured product): channel, name, on the wire, carries. */
const CHANNELS: Partial<Record<Agent, [Channel, string, string, string][]>> = {
  "claude-code": [
    ["model", "Model calls", "POST /v1/messages", "Each agent-loop step; a thread’s first request also carries the tool catalog, MCP servers, skills and injected context"],
    ["analytics", "Product analytics", "POST /api/event_logging/v2/batch", "~128 event types (tengu_*), ~45 env fields each"],
    ["flags", "Feature flags", "POST /api/eval/sdk-*", "Account attributes, flag values, experiments"],
    ["count", "Token counting", "POST /v1/messages/count_tokens", "Whole files’ text"],
    ["account", "Account / setup", "skills list, MCP registry, bootstrap, profile…", "Not tied to a session"],
  ],
  codex: [
    ["model", "Model calls", "WebSocket /backend-api/codex/responses", "Each turn’s steps; prewarm and title calls are hidden by default"],
    ["analytics", "Analytics", "POST /codex/analytics-events/events", "Turn and tool events, with their turn id"],
    ["metrics", "Metrics (OTel)", "POST /otlp/v1/metrics", "Counters and timings, not tied to a call"],
    ["usage", "Usage & account", "wham/usage, accounts/*", "5-hour and weekly limits, plan, credits"],
    ["catalog", "Catalog", "ps/*, plugins/*, codex/models", "Models, plugins and prompts on offer"],
  ],
};

/** What a product's traffic becomes, and its hosts (§11M 3.2). */
const PRODUCT: Record<Agent, { shape: string; hosts: string }> = {
  "claude-code": { shape: "sessions → prompts → steps", hosts: "api.anthropic.com · statsig.anthropic.com · claude.ai/api/oauth" },
  codex: { shape: "threads → turns (shown as prompts) → steps", hosts: "chatgpt.com/backend-api/codex (WebSocket + HTTP) · ab.chatgpt.com" },
  chatgpt: { shape: "conversations → messages", hosts: "chatgpt.com/backend-api/conversation" },
  "claude-ai": { shape: "conversations → messages", hosts: "claude.ai/api" },
};
const TOKENS = { exact: "exact tokens", estimated: "≈ tokens", missing: "no token counts" } as const;

/** A titled block of About this data: a micro-caps heading, an optional line under it, its body. */
export function AboutSection({ title, description, children }: { title: string; description?: string; children: ReactNode }) {
  return (
    <section className="about-section flex flex-col">
      <h2 className="type-key text-ink-faint">{title}</h2>
      {description && <p className="type-body text-ink-muted">{description}</p>}
      {children}
    </section>
  );
}

/** One captured product (§11M 3.2): agent dot and name; shape and tokens (`· no traffic in this window` without sessions); hosts. */
export function AboutProduct({ product: p }: { product: AboutProductData }) {
  const { shape, hosts } = PRODUCT[p.agent];
  return (
    <div className="about-product border-b border-line last:border-b-0">
      <span className="about-product-name type-data-strong flex items-center">
        <span aria-hidden className={`about-product-dot rounded-pill ${AGENT_BG[p.agent]}`} />
        {AGENT_NAME[p.agent]}
      </span>
      <div className="about-product-what">
        <span>
          {`${shape} · ${TOKENS[p.tokenAccuracy]}`}
          {p.sessions === 0 && <span className="text-ink-faint"> · no traffic in this window</span>}
        </span>
        <span className="text-ink-muted">{hosts}</span>
      </div>
    </div>
  );
}

/**
 * What is kept (§11E.1, §11N.1), the one place the deployment's mode is said. Without
 * bodies: `METADATA ONLY`. Claude Code's own analytics report each command's program and
 * exit code in any mode, so those are listed as kept; no tool input is.
 */
export function RetentionList({ contentRetained }: { contentRetained: boolean | null }) {
  return (
    <div className="about-table bg-surface border border-line">
      <Row k="Mode">
        {contentRetained === null ? (
          <span className="text-ink-muted">—</span>
        ) : contentRetained ? (
          <>
            <Chip>full interception</Chip> <span className="text-ink-muted">bodies are recorded on this deployment</span>
          </>
        ) : (
          <>
            <Chip>metadata only</Chip> <span className="text-ink-muted">bodies are not recorded on this deployment</span>
          </>
        )}
      </Row>
      <Row k="Kept">
        {contentRetained
          ? "everything below, plus request and response bodies: prompt text, full commands and exit codes, patch files, responses, Codex titles"
          : "headers, token counts, timings, model and tool names with counts per call, each agent’s own analytics events as sent (Claude Code’s include each command’s program and exit code)"}
      </Row>
      {!contentRetained && (
        <Row k="Not kept">tool inputs (full commands, paths, URLs), Codex’s exit codes, prompt text, responses, file contents, Codex titles, shell and timezone</Row>
      )}
      <Row k="Credentials">always redacted; keys appear only as a hash</Row>
    </div>
  );
}

const Node = ({ name, sub, kind }: { name: string; sub: string; kind?: "agent" | "proxy" }) => (
  <div className={`about-node bg-surface-raised border ${kind === "agent" ? "border-dashed border-line-strong" : kind === "proxy" ? "border-ink" : "border-line-strong"}`}>
    <div className="type-data-strong">{name}</div>
    <div className="about-node-sub text-ink-muted">{sub}</div>
  </div>
);

const Arrow = ({ glyph }: { glyph: string }) => <div className="about-arrow type-data text-ink-faint">{glyph}</div>;

/** The pipeline (§1) as boxes and text glyphs: no SVG. */
function PipelineDiagram() {
  return (
    <div className="about-pipeline">
      <Node name={NODES.agent[0]} sub={NODES.agent[1]} kind="agent" />
      <Arrow glyph="→" />
      <Node name={NODES.proxy[0]} sub={NODES.proxy[1]} kind="proxy" />
      <Arrow glyph="→" />
      <Node name={NODES.vendor[0]} sub={NODES.vendor[1]} />
      <div />
      <div />
      <div className="about-copy text-ink-faint">↓ copy of every request and response</div>
      <div />
      <div />
      <Node name={NODES.ui[0]} sub={NODES.ui[1]} />
      <Arrow glyph="←" />
      <Node name={NODES.observer[0]} sub={NODES.observer[1]} />
      <div />
      <div />
      <div className="about-bypass border border-dashed border-danger text-danger">
        ▲ Traffic that skips the proxy (for example over IPv6) is never seen. AIntercept finds it later in Claude Code’s own analytics and lists it as “reported, not captured”.
      </div>
    </div>
  );
}

/** A product's channels: channel, on the wire, what it carries. */
function ChannelTable({ agent }: { agent: Agent }) {
  return (
    <div className="about-table bg-surface border border-line">
      <div className="about-channel about-channel-head type-key text-ink-faint border-b border-line">
        <span className="flex items-center about-channel-name">
          <span aria-hidden className={`about-product-dot rounded-pill ${AGENT_BG[agent]}`} />
          {AGENT_NAME[agent]}
        </span>
        <span>On the wire</span>
        <span>Carries</span>
      </div>
      {(CHANNELS[agent] ?? []).map(([ch, name, wire, carries]) => (
        <div key={ch} className="about-channel border-b border-line">
          <span className="about-channel-name type-data-strong flex items-center">
            <ChannelSwatch channel={ch} />
            {name}
          </span>
          <span className="about-wire text-ink-muted">{wire}</span>
          <span className="about-carries text-ink-muted">{carries}</span>
        </div>
      ))}
    </div>
  );
}

const Row = ({ k, children, kTone = "muted" }: { k: ReactNode; children: ReactNode; kTone?: "muted" | "plain" }) => (
  <div className="about-row border-b border-line last:border-b-0">
    <span className={`about-row-k ${kTone === "muted" ? "text-ink-muted" : ""}`}>{k}</span>
    <span className="about-row-v">{children}</span>
  </div>
);

/**
 * "About this data" (handoff §11E.1): a 640px drawer that explains, once, what
 * AIntercept sees, how, what it keeps, and what the marks on every page mean.
 * Static content: it opens at once. Esc and × close it; focus is trapped
 * inside and returns to the opener.
 */
export function AboutThisData({ contentRetained, products, onClose }: AboutThisDataProps) {
  const opener = useRef<Element | null>(null);
  // Focus the heading on open; give focus back to the opener on close.
  useLayoutEffect(() => {
    opener.current = document.activeElement;
    document.getElementById("about-title")?.focus();
    return () => (opener.current instanceof HTMLElement ? opener.current.focus() : undefined);
  }, []);
  const onKey = (e: KeyboardEvent<HTMLElement>) => {
    if (e.key === "Escape") return onClose();
    trapTab(e);
  };
  return (
    <>
      <div className="drawer-scrim" onClick={onClose} aria-hidden />
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
      <section role="dialog" aria-modal="true" aria-labelledby="about-title" className="about-drawer bg-surface border-l border-line-strong" onKeyDown={onKey}>
        <div className="about-header flex items-center bg-surface-raised border-b border-line">
          <h1 id="about-title" tabIndex={-1} className="type-data-strong text-ink">
            About this data
          </h1>
          <span className="flex-1" />
          <Button variant="icon" aria-label="Close" onClick={onClose}>
            ×
          </Button>
        </div>
        {/* Scrolls on its own: focusable, so the keyboard can scroll it. */}
        {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
        <div className="about-body flex flex-col" role="region" aria-label="About this data" tabIndex={0}>
          <p className="type-body text-ink-muted">What AIntercept sees, how it gets it, what it keeps, and what the marks on every page mean.</p>
          {products && products.length > 0 && (
            <AboutSection title="Products">
              <div className="about-table bg-surface border border-line">
                {products.map((p) => (
                  <AboutProduct key={p.agent} product={p} />
                ))}
              </div>
            </AboutSection>
          )}
          <AboutSection title="How the data gets here">
            <PipelineDiagram />
          </AboutSection>
          <AboutSection title="Channels">
            {(products?.length ? products.map((p) => p.agent) : (["claude-code"] as Agent[]))
              .filter((a) => CHANNELS[a])
              .map((a) => (
                <ChannelTable key={a} agent={a} />
              ))}
          </AboutSection>
          <AboutSection title="What is kept">
            <RetentionList contentRetained={contentRetained} />
          </AboutSection>
          <AboutSection title="What the marks mean">
            <div className="about-table bg-surface border border-line">
              <Row k={<span className="text-ink-muted">—</span>} kTone="plain">
                Not known. Point at it for the reason (not reported, not extracted yet, or not sent by this agent). Never shown as 0.
              </Row>
              <Row k={<span className="text-ink-muted">≈ 610k</span>} kTone="plain">
                Estimated from text size, because the agent doesn’t report token usage.
              </Row>
              <Row k={<Chip tone="notRetained">not retained</Chip>} kTone="plain">
                Content not kept; its shape (size, counts, types) is shown instead.
              </Row>
              <Row k={<span className="text-danger">▲ 94</span>} kTone="plain">
                Coverage gap: Claude Code reported calls the proxy never saw. Red means only this, or a destructive command.
              </Row>
            </div>
          </AboutSection>
        </div>
      </section>
    </>
  );
}
