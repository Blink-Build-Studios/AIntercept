import type { Meta } from "@storybook/react";
import { EventRow } from "./EventRow";

// Analytics events aren't stored any more (they stay in the raw files), so no sample holds an event row:
// the Events tab and the drawer's event list are empty. The component's look is kept by its canvas
// specimens (EventRow.canvas.stories.tsx) until the redraw decides its fate.
const meta = {
  title: "Components/Rows/EventRow",
  component: EventRow,
  parameters: { layout: "padded" },
} satisfies Meta<typeof EventRow>;
export default meta;
