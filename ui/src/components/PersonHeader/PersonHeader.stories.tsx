import type { Meta, StoryObj } from "@storybook/react";
import { personHeaders } from "../ProviderMark/providerSpecimens";
import { PersonHeader } from "./PersonHeader";

const meta = { title: "Components/Providers/PersonHeader", component: PersonHeader, parameters: { layout: "padded" } } satisfies Meta<typeof PersonHeader>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A Codex-only person: the All agents row, then one Codex row. */
export const CodexOnly: Story = { args: personHeaders.codexOnly, decorators: [(Story) => <div style={{ width: 1344 }}>{Story()}</div>] };
/** A linked person: Claude Code and Codex rows under All agents. */
export const Mixed: Story = { args: personHeaders.mixed, decorators: [(Story) => <div style={{ width: 1344 }}>{Story()}</div>] };
/** At 1024px each row's stats wrap within it, aligned to the stat area's left edge. */
export const MixedNarrow: Story = { args: personHeaders.mixed, decorators: [(Story) => <div style={{ width: 1024 }}>{Story()}</div>] };
