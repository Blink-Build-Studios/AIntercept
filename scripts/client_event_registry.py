#!/usr/bin/env python3
"""Merge observed client-event shapes into the observer's client-event registries.

Usage: client_event_registry.py REGISTRY_DIR DIR [DIR ...]

REGISTRY_DIR holds one file per client source (claude_code.json, claude_web.json,
codex.json, chatgpt_web.json, otel.json). Reads gzipped NDJSON under each DIR:
client_event rows (phase=client_event, from the observer), and, if present, dev
full-body capture rows carrying a client's analytics batches. Every event name and leaf
field path seen is ADDED to its source's registry; nothing is ever removed, so
older clients' shapes stay known. Paths are built exactly as the observer builds them
(clientEventPaths): nested keys joined by ".", array elements as "[]".
"common" holds paths seen across nearly every event name (env, auth, …).
"""
import base64, collections, gzip, json, os, sys, zlib


def decode_embedded(d):
    """Mirror the observer: base64-encoded JSON objects (prefix eyJ) decoded."""
    for k, v in list(d.items()):
        if isinstance(v, str) and v.startswith("eyJ"):
            try:
                obj = json.loads(base64.b64decode(v + "=" * (-len(v) % 4)))
                if isinstance(obj, dict):
                    d[k] = obj
            except Exception:
                pass
    return d


def paths(v, p="", out=None):
    out = set() if out is None else out
    if isinstance(v, dict):
        for k, c in v.items():
            paths(c, f"{p}.{k}" if p else k, out)
    elif isinstance(v, list):
        for c in v:
            paths(c, p + "[]", out)
    elif p:
        out.add(p)
    return out


# Raw analytics batch paths in dev captures, by client source.
CAPTURE_PATHS = {"/api/event_logging/": "claude_code", "/backend-api/codex/analytics-events/": "codex"}
CHATGPT_WEB_PATHS = ("/ces/v1/rgstr", "/ces/v1/telemetry/intake", "/ces/statsc/flush")


def capture_source(host, path):
    """Mirror of the observer's clientEventParsers matching."""
    path = (path or "").split("?")[0]
    if host == "claude.ai" and path.startswith("/api/event_logging/"):
        return "claude_web"
    if host == "claude.ai" and path == "/api/v2/rum":
        return "claude_web_rum"
    if host == "chatgpt.com" and path in CHATGPT_WEB_PATHS:
        return "chatgpt_web"
    if path.endswith("/v1/metrics"):
        return "otel"
    for prefix, source in CAPTURE_PATHS.items():
        if path.startswith(prefix):
            return source
    return None


def _str(v):
    """Go's `s, _ := v.(string)`: the value if it's a string, else ""."""
    return v if isinstance(v, str) else ""


def _obj(v):
    """Go's `m, _ := v.(map[string]any)`: the value if it's an object, else {}."""
    return v if isinstance(v, dict) else {}


def decode_request(raw, encoding):
    """Undo a captured request body's content-encoding, as the observer does.
    Brotli isn't in the standard library; such bodies are skipped (None)."""
    enc = (encoding or "").strip().lower()
    try:
        if "gzip" in enc:
            return gzip.decompress(raw)
        if enc == "zstd":
            try:
                from compression import zstd  # Python 3.14+
            except ImportError:
                return None
            return zstd.decompress(raw)
        if enc == "br":
            return None
    except Exception:
        return None
    return raw


def rum_events(raw):
    """Mirror of the observer's parseRUMEvents: yields (name, data)."""
    if raw[:1] == b"\x78":
        try:
            raw = zlib.decompress(raw)
        except zlib.error:
            pass
    for line in raw.decode("utf-8", "replace").splitlines():
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if not isinstance(d, dict):
            continue
        t = _str(d.get("type"))
        sub = {"action": "type", "resource": "type", "vital": "name"}.get(t)
        yield (f"{t}:{_str(_obj(d.get(t)).get(sub))}" if sub else t), d


def chatgpt_web_events(path, raw):
    """Mirror of the observer's parseChatGPTWebAnalytics: yields (name, data)."""
    if path == "/ces/v1/telemetry/intake":
        for line in raw.decode("utf-8", "replace").splitlines():
            try:
                d = json.loads(line)
            except ValueError:
                continue
            if isinstance(d, dict):
                yield f"{_str(_obj(d.get('logger')).get('name'))}:{_str(d.get('kind')) or _str(d.get('message'))}", d
        return
    try:
        j = json.loads(raw)
    except ValueError:
        return
    if path == "/ces/statsc/flush":
        if not isinstance(j, dict):
            return
        for kind in ("counters", "histograms"):
            for m in j.get(kind) or []:
                if not isinstance(m, dict):
                    continue
                m["client_type"] = j.get("client_type")
                yield f"{_str(m.get('namespace'))}.{_str(m.get('metric'))}", m
        return
    for ev in _obj(j).get("events") or []:
        ev = _obj(ev)
        name = _str(ev.get("eventName"))
        if name == "__protobuf_structured_event__":
            t = _str(_obj(_obj(ev.get("metadata")).get("eventParams")).get("@type"))
            name = t.rsplit(".", 1)[-1] if t else name
        # Like the observer, keep nameless events (registered under "").
        yield name, ev


def _otlp_num(v):
    if isinstance(v, str):
        for conv in (int, float):
            try:
                return conv(v)
            except ValueError:
                pass
    return v


def _otlp_attrs(kvs):
    out = {}
    for kv in kvs or []:
        for typ, v in (kv.get("value") or {}).items():
            out[kv["key"]] = _otlp_num(v) if typ in ("intValue", "doubleValue") else v
    return out


def otlp_metrics(export):
    """Mirror of the observer's parseOTLPMetrics: yields (metric name, data)."""
    for rm in export.get("resourceMetrics") or []:
        resource = _otlp_attrs((rm.get("resource") or {}).get("attributes"))
        for sm in rm.get("scopeMetrics") or []:
            for m in sm.get("metrics") or []:
                data = {"unit": m.get("unit", ""), "description": m.get("description", ""), "resource": resource}
                if sm.get("scope"):
                    data["scope"] = sm["scope"]
                for kind in ("sum", "gauge", "histogram", "exponentialHistogram", "summary"):
                    if kind not in m:
                        continue
                    body = m[kind] or {}
                    data["kind"] = kind
                    if body.get("aggregationTemporality") is not None:
                        data["temporality"] = body["aggregationTemporality"]
                    if body.get("isMonotonic") is not None:
                        data["monotonic"] = body["isMonotonic"]
                    pts = []
                    for dp in body.get("dataPoints") or []:
                        p = {}
                        for k, v in dp.items():
                            if k == "attributes":
                                p["attributes"] = _otlp_attrs(v)
                            elif k in ("asInt", "asDouble"):
                                p["value"] = _otlp_num(v)
                            elif k in ("count", "sum", "min", "max", "startTimeUnixNano", "timeUnixNano"):
                                p[k] = _otlp_num(v)
                            else:
                                p[k] = v
                        pts.append(p)
                    data["points"] = pts
                    break
                yield m.get("name"), data


undecodable = collections.Counter()  # captured batches we couldn't decode, by source


def events_from(dirs):
    capture = collections.defaultdict(list)  # correlation id -> [(seq, bytes)]
    batch_cids = {}  # correlation id -> source
    encodings = {}  # correlation id -> request content-encoding
    for root in dirs:
        for dp, _, fs in os.walk(root):
            for f in fs:
                if not f.endswith(".gz"):
                    continue
                with gzip.open(os.path.join(dp, f), "rt") as fh:
                    for line in fh:
                        try:
                            e = json.loads(line)
                        except ValueError:
                            continue
                        ce = e.get("client_event")
                        if e.get("phase") == "client_event" and ce and ce.get("name"):
                            source = ce.get("source") or "claude_code"
                            if source == "claude_code" and e.get("host") == "claude.ai":
                                source = "claude_web"  # rows from before claude_web existed
                            yield source, ce["name"], ce.get("data") or {}
                        elif e.get("phase") == "request_headers":
                            hdrs = {k.lower(): v for k, v in (e.get("headers") or {}).items()}
                            if hdrs.get("content-encoding"):
                                encodings[e.get("correlation_id")] = hdrs["content-encoding"]
                        if e.get("phase") == "request_body_chunk" and e.get("body_b64") \
                                and capture_source(e.get("host"), e.get("path")):
                            capture[e["correlation_id"]].append((e["seq"], base64.b64decode(e["body_b64"])))
                            batch_cids[e["correlation_id"]] = (capture_source(e.get("host"), e.get("path")),
                                                               (e.get("path") or "").split("?")[0])
    for cid, (source, path) in batch_cids.items():
        raw = decode_request(b"".join(b for _, b in sorted(capture[cid])), encodings.get(cid))
        if raw is None:
            undecodable[source] += 1
            continue
        if source == "chatgpt_web":
            for name, data in chatgpt_web_events(path, raw):
                yield source, name, data
            continue
        if source == "claude_web_rum":
            for name, data in rum_events(raw):
                yield "claude_web", name, data
            continue
        try:
            batch = json.loads(raw)
        except ValueError:
            continue
        if source == "otel":
            for name, data in otlp_metrics(batch):
                yield source, _str(name), data
            continue
        for ev in _obj(batch).get("events") or []:
            ev = _obj(ev)
            if source == "codex":
                yield source, _str(ev.get("event_type")), decode_embedded(_obj(ev.get("event_params")))
            else:
                d = _obj(ev.get("event_data"))
                yield source, _str(d.get("event_name")) or _str(ev.get("event_type")), decode_embedded(d)


def merge(reg_path, observed):
    reg = {"common": [], "events": {}}
    if os.path.exists(reg_path):
        with open(reg_path) as fh:
            reg = json.load(fh)
    common = set(reg.get("common", []))
    events = {k: set(v) for k, v in reg.get("events", {}).items()}
    before = {k: len(v) for k, v in events.items()}
    for name, ps in observed.items():
        events.setdefault(name, set()).update(ps)
    # Common: paths present under at least 90% of event names (min 5 names).
    if len(events) >= 5:
        counts = collections.Counter(p for ps in events.values() for p in ps | common)
        common |= {p for p, c in counts.items() if c >= 0.9 * len(events)}
    out = {"common": sorted(common), "events": {n: sorted(ps - common) for n, ps in sorted(events.items())}}
    with open(reg_path, "w") as fh:
        json.dump(out, fh, indent=1, sort_keys=True)
        fh.write("\n")
    new_names = [n for n in events if n not in before]
    grown = [n for n in events if n in before and len(events[n] - common) > before[n]]
    return len(out["events"]), len(out["common"]), len(new_names), len(grown)


def main():
    reg_dir, dirs = sys.argv[1], sys.argv[2:]
    observed = collections.defaultdict(lambda: collections.defaultdict(set))
    seen = collections.Counter()
    for source, name, data in events_from(dirs):
        seen[source] += 1
        observed[source][name].update(paths(data))
    for source in sorted(observed):
        n, c, new, grown = merge(os.path.join(reg_dir, source + ".json"), observed[source])
        print(f"{source}: read {seen[source]} events; registry now {n} events, {c} common paths; "
              f"new names: {new}, names with new fields: {grown}")
    for source, n in sorted(undecodable.items()):
        print(f"{source}: skipped {n} captured batches with an encoding this script can't decode")


if __name__ == "__main__":
    main()
