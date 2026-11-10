import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import failed from "../../api/samples/request/failed";
import normal from "../../api/samples/request/normal";
import notCaptured from "../../api/samples/request/not_captured";
import { CopyButton } from "../CopyButton/CopyButton";
import { statusLine } from "./drawerData";
import { DrawerHeader } from "./DrawerHeader";

const meta = {
  title: "Components/Drawer/DrawerHeader",
  component: DrawerHeader,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 820 }}>{Story()}</div>],
  args: { channel: "model", onPrev: fn(), onNext: fn(), onClose: fn(), actions: <CopyButton label="Copy as curl" text={() => "curl …"} /> },
} satisfies Meta<typeof DrawerHeader>;
export default meta;
type Story = StoryObj<typeof meta>;

const of = (d: typeof normal.data) => ({ method: d.method, path: d.path, kind: d.kind, status: statusLine(d) });
export const Normal: Story = { args: of(normal.data) };
export const NotCaptured: Story = { args: of(notCaptured.data) };
export const Failed: Story = { args: of(failed.data) };
/** At the end of the range: ↓ is disabled with its reason. */
export const LastInRange: Story = { args: { ...of(normal.data), onNext: undefined } };
