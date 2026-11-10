import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import requests from "../../api/samples/person_requests/demo";
import { Pagination } from "./Pagination";

const total = requests.meta.total ?? 0;
const size = requests.meta.pageSize ?? 50;
const count = Math.ceil(total / size);
const at = (page: number) => ({ page, pageCount: count, from: (page - 1) * size + 1, to: Math.min(page * size, total), total, onPage: fn() });

const meta = { title: "Components/Controls/Pagination", component: Pagination, parameters: { layout: "padded" }, args: at(1) } satisfies Meta<
  typeof Pagination
>;
export default meta;
type Story = StoryObj<typeof meta>;

export const First: Story = {};
export const Middle: Story = { args: at(12) };
export const Last: Story = { args: at(count) };
export const Single: Story = { args: { page: 1, pageCount: 1, from: 1, to: 7, total: 7, onPage: fn() } };
export const Loading: Story = { args: { ...at(3), loading: true } };
export const NewAfterEnd: Story = { args: { ...at(count), newAfterEnd: 12 } };
