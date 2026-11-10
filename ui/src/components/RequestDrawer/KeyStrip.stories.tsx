import type { Meta, StoryObj } from "@storybook/react";
import failed from "../../api/samples/request/failed";
import normal from "../../api/samples/request/normal";
import { codexKeyTiles } from "../ProviderMark/providerSpecimens";
import { keyCells } from "./drawerData";
import { KeyStrip } from "./KeyStrip";

const meta = {
  title: "Components/Drawer/KeyStrip",
  component: KeyStrip,
  parameters: { layout: "padded" },
} satisfies Meta<typeof KeyStrip>;
export default meta;
type Story = StoryObj;

// KeyStrip's props are a union (Claude Code's cells, or a provider's tiles), so each story renders it.
const at = (width: number, strip: React.ReactNode): Story => ({ render: () => <div style={{ width }}>{strip}</div> });

export const Values: Story = at(822, <KeyStrip cells={keyCells(normal.data)} />);
/** A failed call: no tokens, cost or first byte, each with its reason. */
export const Unknown: Story = at(822, <KeyStrip cells={keyCells(failed.data)} />);
/** Codex (§11L.10): Model, Tokens, Reasoning, TTFT, Duration; no Cost tile. */
export const CodexCall: Story = at(640, <KeyStrip tiles={codexKeyTiles.call} />);
/** A Codex call with no usage: Tokens and Reasoning unknown, `not captured`. */
export const CodexNoUsage: Story = at(640, <KeyStrip tiles={codexKeyTiles.noUsage} />);
