import type { Meta, StoryObj } from "@storybook/react";
import unattributed from "../../api/samples/overview/unattributed";
import { UnattributedNotice } from "./UnattributedNotice";

const meta = {
  title: "Components/Summary/UnattributedNotice",
  component: UnattributedNotice,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 706 }}>{Story()}</div>],
} satisfies Meta<typeof UnattributedNotice>;
export default meta;

const u = unattributed.data.unattributed;
export const Default: StoryObj<typeof meta> = {
  args: { sessions: u.sessions, modelCalls: u.modelCalls, firstSeen: u.firstSeen ?? unattributed.meta.asOf, reason: u.reason, href: "#unattributed" },
};
