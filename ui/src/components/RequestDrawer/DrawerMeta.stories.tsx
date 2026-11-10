import type { Meta, StoryObj } from "@storybook/react";
import normal from "../../api/samples/request/normal";
import { metaParts } from "./drawerData";
import { DrawerMeta } from "./DrawerMeta";

const meta = { title: "Components/Drawer/DrawerMeta", component: DrawerMeta, parameters: { layout: "padded" } } satisfies Meta<typeof DrawerMeta>;
export default meta;

export const Default: StoryObj<typeof meta> = { args: { parts: metaParts(normal.data) } };
