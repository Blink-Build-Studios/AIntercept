import type { Meta, StoryObj } from "@storybook/react";
import { HeaderList } from "./HeaderList";
import { HEADER_SPECIMENS } from "./headerSpecimens";
import { codexResponseHeaders } from "../ProviderMark/providerSpecimens";

const meta = {
  title: "Components/Drawer/HeaderList",
  component: HeaderList,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 820 }}>{Story()}</div>],
} satisfies Meta<typeof HeaderList>;
export default meta;
type Story = StoryObj<typeof meta>;

const box = "border border-line bg-surface";
/** A few request headers: the name column is the longest name. */
export const Short: Story = { args: { headers: HEADER_SPECIMENS.short, className: box } };
/** A real-sized Anthropic response: the shared `anthropic-ratelimit-unified-` prefix is faint. */
export const AnthropicResponse: Story = { args: { headers: HEADER_SPECIMENS.anthropicResponse, className: box } };
/** Long values wrap anywhere inside the value column. */
export const LongValue: Story = { args: { headers: HEADER_SPECIMENS.longValue, className: box } };
/** Credentials are redacted server-side; the key hash stands in, faint. */
export const Redacted: Story = { args: { headers: HEADER_SPECIMENS.redacted, className: box } };
/** A name over the 400px cap: a middle ellipsis, the full name in a tooltip. */
export const OverCap: Story = {
  args: { headers: [{ name: "x-a-very-long-vendor-header-name-that-goes-on-and-on-past-the-cap", value: "1" }, ...HEADER_SPECIMENS.short.slice(0, 2)], className: box },
};

/** Codex response headers (§11L.8): no prefix is dimmed; names ink, values muted, wire order. */
export const NoDimming: Story = { args: { headers: codexResponseHeaders } };
