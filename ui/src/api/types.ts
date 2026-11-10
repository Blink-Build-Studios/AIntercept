// Convenience aliases over the generated contract. Never hand-write API types:
// change the Pydantic models in api/ and run `make regen`.
import type { components } from "./spec";

export type Schemas = components["schemas"];
export type StatusResponse = Schemas["StatusResponse"];

// Session page (handoff §5.2.7-5.2.9).
export type SessionResponse = Schemas["SessionResponse"];
export type SessionHeader = Schemas["SessionHeader"];
export type SessionTimelineResponse = Schemas["SessionTimelineResponse"];
export type SessionTimeline = Schemas["SessionTimeline"];
export type SessionStateResponse = Schemas["SessionStateResponse"];
export type SessionState = Schemas["SessionState"];
export type SessionPromptsResponse = Schemas["SessionPromptsResponse"];
export type PromptItem = Schemas["PromptItem"];
export type BetweenItem = Schemas["BetweenItem"];
export type PromptData = Schemas["PromptData"];
export type StepData = Schemas["StepData"];
export type ToolCallData = Schemas["ToolCallData"];
export type BetweenData = Schemas["BetweenData"];
export type SessionRequestsResponse = Schemas["SessionRequestsResponse"];
export type SessionEventsResponse = Schemas["SessionEventsResponse"];
export type EventRow = Schemas["EventRow"];
