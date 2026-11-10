import type { Meta, StoryObj } from "@storybook/react";
import summary from "../../api/samples/person_summary/demo";
import { formatCount } from "../../lib/format";
import { StatRibbon } from "./StatRibbon";

const { ribbon: rb, coverage: cov } = summary.data;
const tiles = [
  { label: "Model calls", swatch: "model" as const, value: rb.modelCalls, sub: `${rb.sessions} session · ${cov.notCaptured} not captured` },
  { label: "Analytics", swatch: "analytics" as const, value: rb.analyticsEvents, sub: `${rb.analyticsBatches} batches · ${rb.eventTypes} types` },
  { label: "Account calls", swatch: "account" as const, value: rb.accountCalls, sub: "no session" },
  { label: "Reach", swatch: "skills" as const, value: rb.toolsReachable, unknownReason: "not extracted yet" },
  { label: "Tokens out", swatch: "count" as const, value: rb.tokens.output, sub: `${formatCount(rb.tokens.cacheRead ?? 0)} cache read` },
  { label: "Cost est.", value: rb.cost.usd === null ? null : `$${rb.cost.usd}`, sub: "API-equivalent", unknownReason: "no cost reported" },
];

const meta = { title: "Components/Summary/StatRibbon", component: StatRibbon, parameters: { layout: "padded" } } satisfies Meta<
  typeof StatRibbon
>;
export default meta;
type Story = StoryObj<typeof meta>;

/** From the demo sample (real traffic, sanitized). */
export const Default: Story = { args: { tiles } };
export const Loading: Story = { args: { tiles, loading: true } };
