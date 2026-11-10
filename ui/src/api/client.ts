import type { Schemas } from "./types";

export const API_BASE = "/api/v1";
/** Hard ceiling per request (handoff §6A.1: abort at 30 s, keep existing data). */
export const REQUEST_TIMEOUT_MS = 30_000;

export type ErrorResponse = Schemas["ErrorResponse"];

export class ApiError extends Error {
  constructor(
    readonly status: number | null,
    readonly body: ErrorResponse | null,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
  get queryId(): string | undefined {
    return this.body?.error.queryId;
  }
}

/**
 * GET a contract endpoint. `signal` comes from TanStack Query, so superseded
 * queries cancel. Every failure is an ApiError, except cancellation by the
 * caller, which re-throws the AbortError so the query layer ignores it.
 */
export async function getJson<T>(path: string, signal?: AbortSignal, fetchImpl: typeof fetch = fetch): Promise<T> {
  const timeout = AbortSignal.timeout(REQUEST_TIMEOUT_MS);
  const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;

  // Covers both the request and reading the body: a stall mid-body is a timeout too.
  const fail = (e: unknown, status: number | null): never => {
    if (signal?.aborted) throw e;
    if (timeout.aborted) throw new ApiError(status, null, "Timed out after 30 s");
    if (e instanceof ApiError) throw e;
    throw new ApiError(status, null, status === null ? "Couldn't reach the API" : "Unexpected response from the API");
  };

  let res: Response;
  try {
    res = await fetchImpl(`${API_BASE}${path}`, { signal: combined, headers: { accept: "application/json" } });
  } catch (e) {
    return fail(e, null);
  }
  let body: unknown;
  try {
    body = await res.json();
  } catch (e) {
    if (!res.ok) throw new ApiError(res.status, null, `HTTP ${res.status}`);
    return fail(e, res.status);
  }
  if (!res.ok) {
    const err = (body as ErrorResponse | null)?.error ? (body as ErrorResponse) : null;
    throw new ApiError(res.status, err, err?.error.message ?? `HTTP ${res.status}`);
  }
  return body as T;
}
