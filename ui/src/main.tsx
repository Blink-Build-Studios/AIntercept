// First: global.css declares the cascade layer order, which any component CSS imported earlier would set instead.
import "./design/global.css";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, Navigate, RouterProvider } from "react-router-dom";
import { TooltipProvider } from "./components/Tooltip/Tooltip";
import { PeoplePage } from "./pages/PeoplePage/PeoplePage";
import { PersonPage } from "./pages/PersonPage/PersonPage";
import { SessionPage } from "./pages/SessionPage/SessionPage";

// Routes (handoff §7): each page wires one screen.
const router = createBrowserRouter([
  { path: "/", element: <Navigate to="/people" replace /> },
  { path: "/people", element: <PeoplePage /> },
  { path: "/people/:personId", element: <PersonPage /> },
  { path: "/sessions/:sessionId", element: <SessionPage /> },
]);

// Polling and retries are the pages' (useLive): no refetch on focus, no silent retries.
const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });

const root = document.getElementById("root");
if (!root) throw new Error("index.html has no #root element");

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <RouterProvider router={router} />
      </TooltipProvider>
    </QueryClientProvider>
  </StrictMode>,
);
