import { useState } from "react";
import { formatBytes, formatCount } from "../../lib/format";
import { HeaderList } from "./HeaderList";
import { headers, type Raw } from "./drawerData";
import { SegmentedControl } from "../SegmentedControl/SegmentedControl";
import { StreamEventList } from "./StreamEventList";
import "./RawView.css";

/** A JSON body, indented (pretty) or as sent (raw); anything that isn't JSON is shown as is. */
function body(text: string, pretty: boolean): string {
  if (!pretty) return text;
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

/**
 * The drawer's Raw tab (handoff §6.6): the request line and headers (credentials
 * redacted server-side), the body when content is retained (pretty or raw), the
 * response line and headers, and the decoded stream. Without wire data, why not.
 */
export function RawView({ raw }: { raw: Raw }) {
  const [pretty, setPretty] = useState(true);
  if (!raw.requestLine && raw.unavailable) return <p className="raw-unavailable type-body text-ink-muted">{raw.unavailable}</p>;
  const reqBytes = raw.requestHeaders.find((h) => h.name === "content-length")?.value;
  return (
    <div className="raw-view">
      {raw.requestLine && <p className="raw-line type-data-strong text-ink">{raw.requestLine}</p>}
      <h3 className="raw-head type-key text-ink-faint">Request headers · {formatCount(raw.requestHeaders.length)}</h3>
      <HeaderList headers={headers(raw.requestHeaders, raw.keyHash)} />
      <div className="raw-body-head">
        <h3 className="type-key text-ink-faint">
          Request body{reqBytes && Number.isFinite(Number(reqBytes)) ? ` · ${formatBytes(Number(reqBytes))}` : ""}
          {raw.requestBody ? "" : " · not retained"}
        </h3>
        <span className="flex-1" />
        {raw.requestBody && (
          <SegmentedControl
            aria-label="Body format"
            value={pretty ? "pretty" : "raw"}
            onChange={(v) => setPretty(v === "pretty")}
            options={[
              { value: "pretty", label: "Pretty" },
              { value: "raw", label: "Raw" },
            ]}
          />
        )}
      </div>
      {raw.requestBody && <pre className="raw-pre type-data bg-bg text-ink">{body(raw.requestBody, pretty)}</pre>}
      {raw.unavailable ? (
        <p className="raw-missing type-body text-ink-muted">{raw.unavailable}</p>
      ) : (
        <>
          {raw.responseLine && <p className="raw-line raw-response-line type-data-strong text-ink">{raw.responseLine}</p>}
          <h3 className="raw-head type-key text-ink-faint">Response headers · {formatCount(raw.responseHeaders.length)}</h3>
          <HeaderList headers={headers(raw.responseHeaders, raw.keyHash)} />
          {raw.responseBody && <pre className="raw-pre type-data bg-bg text-ink">{body(raw.responseBody, pretty)}</pre>}
          <h3 className="raw-head raw-stream-head type-key text-ink-faint">
            {raw.stream ? `Response stream · ${formatCount(raw.stream.length)} server-sent events · decoded` : "Response stream · not retained"}
          </h3>
          {raw.stream && <StreamEventList events={raw.stream} />}
        </>
      )}
    </div>
  );
}
