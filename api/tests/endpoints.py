"""The URLs every endpoint-parity gate walks: each endpoint, for every person and session a
client serves."""


def endpoint_urls(client) -> list[str]:
    urls = ["/api/v1/status", "/api/v1/overview", "/api/v1/people", "/api/v1/people?q=codex"]
    people = [p["personId"] for p in client.get("/api/v1/people").json()["data"]] + ["unattributed", "nobody"]
    sessions: list[str] = []
    for pid in people:
        base = f"/api/v1/people/{pid}"
        urls += [
            base,
            f"{base}/summary",
            f"{base}/sessions",
            f"{base}/sessions?pageSize=1&page=2",
            f"{base}/timeline",
            f"{base}/timeline?binSeconds=60",
            f"{base}/requests",
            f"{base}/requests?pageSize=5&page=3&sort=asc",
            f"{base}/requests?q=model",
            f"/api/v1/coverage?personId={pid}",
        ]
        r = client.get(f"{base}/sessions?pageSize=100").json()
        sessions += [s["sessionId"] for s in r.get("data", [])]
        reqs = client.get(f"{base}/requests?pageSize=3").json().get("data", [])
        for q in reqs:
            urls += [f"/api/v1/requests/{q['requestId']}", f"/api/v1/requests/{q['requestId']}/details"]
    for sid in [*sessions, "no-such-session"]:
        base = f"/api/v1/sessions/{sid}"
        for e in client.get(f"{base}/events?pageSize=3").json().get("data", []):
            if e.get("eventId"):
                urls.append(f"/api/v1/events/{e['eventId']}")
        urls += [
            base,
            f"{base}/timeline",
            f"{base}/state",
            f"{base}/prompts",
            f"{base}/prompts?pageSize=2&page=2",
            f"{base}/requests",
            f"{base}/events",
            f"{base}/events?q=tool&pageSize=10",
        ]
    return urls
