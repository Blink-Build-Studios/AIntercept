import type { Meta, StoryObj } from "@storybook/react";
import sessions from "../../api/samples/person_sessions/many";
import { EmptyState } from "../EmptyState/EmptyState";
import { SESSION_COLUMNS, SESSION_MIN_WIDTH, SessionRow } from "../SessionRow/SessionRow";
import { DataTable } from "./DataTable";

const meta = {
  title: "Components/Rows/DataTable",
  component: DataTable,
  parameters: { layout: "padded" },
  args: { columns: SESSION_COLUMNS, minWidth: SESSION_MIN_WIDTH, label: "Sessions" },
} satisfies Meta<typeof DataTable>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: { children: sessions.data.map((s) => <SessionRow key={s.sessionId} session={s} href="#" />) },
};
export const Loading: Story = { args: { loading: true, skeletonRows: 3 } };
export const Empty: Story = { args: { children: null, empty: <EmptyState title="No sessions in this range" /> } };
