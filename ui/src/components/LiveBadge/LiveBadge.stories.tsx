import type { Meta, StoryObj } from "@storybook/react";
import { useStatus } from "../../api/hooks/useStatus";
import { mock } from "../../api/mocks/handlers";
import delayed from "../../api/samples/status/delayed";
import probesOnly from "../../api/samples/status/probes_only";
import receiving from "../../api/samples/status/receiving";
import waiting from "../../api/samples/status/waiting";
import { liveStatusFrom, POLL_INTERVAL_S } from "../../lib/live";
import { LiveBadge } from "./LiveBadge";

const meta = {
  title: "Components/Chips/LiveBadge",
  component: LiveBadge,
  parameters: { layout: "padded" },
} satisfies Meta<typeof LiveBadge>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Live: Story = { args: { status: "live", intervalS: POLL_INTERVAL_S } };
/** The heartbeat's dimmest frame (opacity 0.4), pinned. */
export const HeartbeatMid: Story = { args: { status: "live", intervalS: POLL_INTERVAL_S, "data-force": "midBeat" } };
export const Connecting: Story = { args: { status: "connecting" } };
export const Waiting: Story = { args: { status: "waiting" } };
export const Delayed: Story = { args: { status: "delayed", delayedMs: 4 * 60_000 } };
export const DelayedDays: Story = { args: { status: "delayed", delayedMs: 26 * 3_600_000 } };
export const Retrying: Story = { args: { status: "retrying" } };
export const Paused: Story = { args: { status: "paused", newCount: 12 } };

/** Every generated /status sample (api/tests/test_status.py), mapped to its badge. */
export const FromStatusSamples: Story = {
  args: { status: "live" },
  render: () => (
    <div className="flex flex-col gap-2 type-data text-ink-muted">
      {Object.entries({ waiting, probes_only: probesOnly, receiving, delayed }).map(([name, sample]) => (
        <div key={name} className="flex items-center gap-3">
          <span className="text-ink-faint">{name}</span>
          <LiveBadge {...liveStatusFrom(sample)} intervalS={POLL_INTERVAL_S} />
        </div>
      ))}
    </div>
  ),
};

function StatusFromApi() {
  const q = useStatus();
  if (q.isPending) return <LiveBadge status="connecting" />;
  if (q.isError) return <LiveBadge status="retrying" />;
  return <LiveBadge {...liveStatusFrom(q.data)} intervalS={POLL_INTERVAL_S} heartbeat={q.dataUpdatedAt} />;
}

/**
 * Fetches /api/v1/status through MSW, so the Latency toolbar applies:
 * `slow` shows CONNECTING for 9 s, `error` and `offline` show RETRYING.
 */
export const FromApi: Story = { args: { status: "live" }, render: () => <StatusFromApi /> };

export const FromApiDelayed: Story = {
  args: { status: "live" },
  parameters: { msw: { handlers: [mock("/status", delayed)] } },
  render: () => <StatusFromApi />,
};
