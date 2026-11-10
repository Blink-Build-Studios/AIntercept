// [6B.11-11] No element has a native `title` (handoff §6B.1): every story of
// every component and screen is rendered, open tooltips included, and its DOM scanned.
import { composeStories } from "@storybook/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render } from "@testing-library/react";
import type { ComponentType } from "react";
import { describe, expect, it } from "vitest";
import { TooltipProvider } from "../components/Tooltip/Tooltip";

type StoryModule = Parameters<typeof composeStories>[0];
const modules = import.meta.glob<StoryModule>(["../components/**/*.stories.tsx", "../screens/**/*.stories.tsx"], { eager: true });

describe("[6B.11-11] No element in the rendered app has a title attribute", () => {
  for (const [file, mod] of Object.entries(modules)) {
    const stories = Object.entries(composeStories(mod) as Record<string, ComponentType>);
    it.each(stories)(`${file.replace("../", "")} %s`, async (_, Story) => {
      const client = new QueryClient({ defaultOptions: { queries: { retry: false, enabled: false } } });
      await act(async () => {
        render(
          <QueryClientProvider client={client}>
            <TooltipProvider>
              <Story />
            </TooltipProvider>
          </QueryClientProvider>,
        );
      });
      const titled = [...document.querySelectorAll("[title]")].filter((el) => el.tagName !== "title" && !el.closest("svg"));
      expect(titled.map((el) => el.outerHTML.slice(0, 120))).toEqual([]);
    }, 30_000); // every story renders here: the heavy ones need longer than 5 s on a loaded machine
  }
});
