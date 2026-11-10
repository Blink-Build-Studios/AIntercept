// First: global.css declares the cascade layer order, which any component CSS imported earlier would set instead.
import "../src/design/global.css";
import { withThemeByDataAttribute } from "@storybook/addon-themes";
import type { Preview } from "@storybook/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { initialize, mswLoader } from "msw-storybook-addon";
import { useState, type ReactNode } from "react";
import { handlers } from "../src/api/mocks/handlers";
import { LATENCY_MODES, setLatency, type LatencyMode } from "../src/api/mocks/latency";
import { TooltipProvider } from "../src/components/Tooltip/Tooltip";

initialize({ onUnhandledRequest: "bypass" });

function WithQueryClient({ children }: { children: ReactNode }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }));
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const preview: Preview = {
  globalTypes: {
    latency: {
      description: "MSW response latency / failure mode (handoff §6A.11)",
      toolbar: { title: "Latency", icon: "timer", items: [...LATENCY_MODES], dynamicTitle: true },
    },
  },
  initialGlobals: { latency: "fast" satisfies LatencyMode },
  decorators: [
    withThemeByDataAttribute({
      themes: { dark: "dark", light: "light" },
      defaultTheme: "dark",
      attributeName: "data-theme",
    }),
    (Story, ctx) => {
      setLatency(ctx.globals.latency as LatencyMode);
      // A cache per story and latency mode: re-renders (theme, args) keep it,
      // switching latency starts fresh so the new mode is visible.
      return (
        <WithQueryClient key={`${ctx.id}:${String(ctx.globals.latency)}`}>
          <TooltipProvider>
            <Story />
          </TooltipProvider>
        </WithQueryClient>
      );
    },
  ],
  loaders: [mswLoader],
  parameters: {
    msw: { handlers },
    backgrounds: { disable: true },
    layout: "fullscreen",
  },
};

export default preview;
