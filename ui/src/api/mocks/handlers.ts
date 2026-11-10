import { delay, http, HttpResponse } from "msw";
import eventDemo from "../samples/event/demo";
import overviewDemo from "../samples/overview/demo";
import peopleDemo from "../samples/people/demo";
import requestDemo from "../samples/request/demo";
import requestDetailsDemo from "../samples/request_details/demo";
import requestRawDemo from "../samples/request_raw/demo";
import sessionDemo from "../samples/session/demo";
import sessionEventsDemo from "../samples/session_events/demo";
import sessionPromptsDemo from "../samples/session_prompts/demo";
import sessionRequestsDemo from "../samples/session_requests/demo";
import sessionStateDemo from "../samples/session_state/demo";
import sessionTimelineDemo from "../samples/session_timeline/demo";
import statusReceiving from "../samples/status/receiving";
import { respond } from "./latency";

// Default handlers serve the generated samples, always through `respond` so the
// latency toolbar applies. A story picks another scenario by overriding
// `parameters.msw.handlers` with `mock(path, sample)`, e.g.
// `mock("/sessions/:sessionId/prompts", inFlight)`.
export const mock = (path: string, sample: unknown) => http.get(`/api/v1${path}`, () => respond(sample));
/** The endpoint fails with this (generated) error body, whatever the latency toolbar says. */
export const mockError = (path: string, sample: unknown, status = 500) => http.get(`/api/v1${path}`, () => HttpResponse.json(sample as Record<string, unknown>, { status }));
/** The endpoint never answers (a first load in progress). */
export const mockPending = (path: string) => http.get(`/api/v1${path}`, () => delay("infinite"));

export const handlers = [
  mock("/status", statusReceiving),
  mock("/overview", overviewDemo),
  mock("/people", peopleDemo),
  mock("/sessions/:sessionId", sessionDemo),
  mock("/sessions/:sessionId/timeline", sessionTimelineDemo),
  mock("/sessions/:sessionId/state", sessionStateDemo),
  mock("/sessions/:sessionId/prompts", sessionPromptsDemo),
  mock("/sessions/:sessionId/requests", sessionRequestsDemo),
  mock("/sessions/:sessionId/events", sessionEventsDemo),
  mock("/events/:eventId", eventDemo),
  mock("/requests/:requestId", requestDemo),
  mock("/requests/:requestId/raw", requestRawDemo),
  mock("/requests/:requestId/details", requestDetailsDemo),
];
