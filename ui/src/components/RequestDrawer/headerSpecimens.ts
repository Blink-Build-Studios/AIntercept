// The HeaderList specimens' headers from the canvas sheet CompR11.dc.html (handoff §11H.3):
// the sheet's own placeholder values, for the stories. Not API data.
import type { Header } from "./HeaderList";

export const HEADER_SPECIMENS: Record<"short" | "anthropicResponse" | "longValue" | "redacted", Header[]> = {
  "short": [
    {
      "name": "host",
      "value": "api.anthropic.com"
    },
    {
      "name": "content-type",
      "value": "application/json"
    },
    {
      "name": "anthropic-version",
      "value": "2023-06-01"
    },
    {
      "name": "x-claude-code-session-id",
      "value": "4c1a8d2e-77e0-4b1a-9f3c-0e2d5a6b77e0"
    },
    {
      "name": "authorization",
      "value": "[redacted · key_hash 9e2f41…]",
      "redacted": true
    }
  ],
  "anthropicResponse": [
    {
      "name": "date",
      "value": "Sun, 08 Nov 2026 22:07:41 GMT"
    },
    {
      "name": "content-type",
      "value": "text/event-stream; charset=utf-8"
    },
    {
      "name": "transfer-encoding",
      "value": "chunked"
    },
    {
      "name": "connection",
      "value": "keep-alive"
    },
    {
      "name": "cache-control",
      "value": "no-cache"
    },
    {
      "name": "anthropic-ratelimit-unified-status",
      "value": "allowed"
    },
    {
      "name": "anthropic-ratelimit-unified-5h-status",
      "value": "allowed"
    },
    {
      "name": "anthropic-ratelimit-unified-5h-reset",
      "value": "1794189600"
    },
    {
      "name": "anthropic-ratelimit-unified-5h-utilization",
      "value": "0.41"
    },
    {
      "name": "anthropic-ratelimit-unified-5h-surpassed-threshold",
      "value": "false"
    },
    {
      "name": "anthropic-ratelimit-unified-7d-status",
      "value": "allowed"
    },
    {
      "name": "anthropic-ratelimit-unified-7d-reset",
      "value": "1794614400"
    },
    {
      "name": "anthropic-ratelimit-unified-7d-utilization",
      "value": "0.83"
    },
    {
      "name": "anthropic-ratelimit-unified-7d-surpassed-threshold",
      "value": "true"
    },
    {
      "name": "anthropic-ratelimit-unified-representative-claim",
      "value": "seven_day"
    },
    {
      "name": "anthropic-ratelimit-unified-fallback-percentage",
      "value": "0.5"
    },
    {
      "name": "anthropic-ratelimit-unified-reset",
      "value": "1794189600"
    },
    {
      "name": "anthropic-ratelimit-unified-overage-status",
      "value": "rejected"
    },
    {
      "name": "anthropic-ratelimit-unified-overage-disabled-reason",
      "value": "org_level_disabled"
    },
    {
      "name": "request-id",
      "value": "req_011CVxR9mS4t2bE8uJqk7Wn3"
    },
    {
      "name": "strict-transport-security",
      "value": "max-age=31536000; includeSubDomains; preload"
    },
    {
      "name": "anthropic-organization-id",
      "value": "1b9f0c2e-47d1-4a8b-9e02-5f3c7a6d1e02"
    },
    {
      "name": "via",
      "value": "1.1 google"
    },
    {
      "name": "cf-cache-status",
      "value": "DYNAMIC"
    },
    {
      "name": "x-envoy-upstream-service-time",
      "value": "1904"
    },
    {
      "name": "server",
      "value": "cloudflare"
    },
    {
      "name": "cf-ray",
      "value": "99a1c2d3e4f5a6b7-SJC"
    },
    {
      "name": "x-robots-tag",
      "value": "none"
    },
    {
      "name": "content-encoding",
      "value": "gzip"
    },
    {
      "name": "vary",
      "value": "Accept-Encoding"
    },
    {
      "name": "anthropic-ratelimit-unified-5h-overage-in-use",
      "value": "false"
    },
    {
      "name": "x-should-retry",
      "value": "false"
    },
    {
      "name": "alt-svc",
      "value": "h3=\":443\"; ma=86400"
    },
    {
      "name": "report-to",
      "value": "{\"endpoints\":[{\"url\":\"https://a.nel.cloudflare.com/report/v4?s=…\"}],\"group\":\"cf-nel\",\"max_age\":604800}"
    }
  ],
  "longValue": [
    {
      "name": "content-type",
      "value": "application/json"
    },
    {
      "name": "x-amzn-trace-id",
      "value": "Root=1-6730f1a2-4b8c2d1e9f0a3b5c6d7e8f90;Parent=53995c3f42cd8ad8;Sampled=1;Lineage=a87bd80c:0"
    },
    {
      "name": "report-to",
      "value": "{\"endpoints\":[{\"url\":\"https://a.nel.cloudflare.com/report/v4?s=Xk2%2FpQ9vL3mN8bR1tY6wZ4cE7fH0jK5sD2gA9uI3oP\"}],\"group\":\"cf-nel\",\"max_age\":604800}"
    }
  ],
  "redacted": [
    {
      "name": "authorization",
      "value": "[redacted · key_hash 9e2f41…]",
      "redacted": true
    },
    {
      "name": "x-api-key",
      "value": "[redacted · key_hash 3a0c77…]",
      "redacted": true
    }
  ]
};
