import errorSample from "./samples/status/error";
import receiving from "./samples/status/receiving";
import { ApiError, getJson } from "./client";

const json = (body: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } }));

// Resolves to the rejection; fails the test if the call succeeded.
const failure = (p: Promise<unknown>): Promise<ApiError> =>
  p.then(
    () => {
      throw new Error("expected the call to fail");
    },
    (e: ApiError) => e,
  );

describe("getJson", () => {
  it("returns the body on 200", async () => {
    await expect(getJson("/status", undefined, () => json(receiving))).resolves.toEqual(receiving);
  });

  it("turns the API's ErrorResponse into an ApiError with its queryId", async () => {
    const err = await failure(getJson("/status", undefined, () => json(errorSample, 500)));
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(500);
    expect(err.queryId).toBe("q_sample");
    expect(err.message).toBe("Query failed");
  });

  it("wraps a non-JSON 200 (e.g. an HTML error page) as an ApiError", async () => {
    const html = () => Promise.resolve(new Response("<html>oops</html>", { status: 200 }));
    const err = await failure(getJson("/status", undefined, html));
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe("Unexpected response from the API");
  });

  it("wraps a non-JSON error status", async () => {
    const err = await failure(getJson("/status", undefined, () => Promise.resolve(new Response("bad gateway", { status: 502 }))));
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
  });

  it("wraps a network failure", async () => {
    const err = await failure(getJson("/status", undefined, () => Promise.reject(new TypeError("Failed to fetch"))));
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe("Couldn't reach the API");
  });

  it("re-throws cancellation by the caller, even mid-body", async () => {
    const ctl = new AbortController();
    const stallBody = () =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () => {
          ctl.abort();
          return Promise.reject(new DOMException("Aborted", "AbortError"));
        },
      } as unknown as Response);
    const err = await failure(getJson("/status", ctl.signal, stallBody));
    expect(err).not.toBeInstanceOf(ApiError);
    expect(err.name).toBe("AbortError");
  });
});
