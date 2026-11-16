"""A minimal Interceptor in Python (docs/BUILDING_AN_INTERCEPTOR.md): redacts
"silly clown" in both directions, refuses the model "forbidden-model", answers
two phrases in what the client sends ("I'm a silly rabbit" asks the model for
"Well, tricks are for kids.", "first we bing" for "then we bong"), and checks the bearer token the observer
sends when INTERCEPTOR_TOKEN is set."""
import json
import logging
import os
import re
from concurrent import futures

import grpc

from interceptor.v1 import interceptor_pb2 as pb
from interceptor.v1 import interceptor_pb2_grpc as pb_grpc

PHRASE = re.compile(rb"(?i)silly clown")
# The rabbit, with a straight or curly apostrophe (raw, or JSON-escaped), or none.
_APOSTROPHE = "(?:'|\u2019|\\\\u0027|\\\\u2019)?".encode()
SILLY_RABBIT = re.compile(rb"(?i)i" + _APOSTROPHE + rb"m a silly rabbit")
# No quotes or backslashes: it goes inside JSON strings as it is.
TRICKS = b"Reply with exactly this sentence and nothing else: Well, tricks are for kids."
# When the newest thing the user said matches, the request asks the model for the answer instead.
ANSWERS = [
    ("silly rabbit", SILLY_RABBIT, TRICKS),
    ("bing", re.compile(rb"(?i)first,? we bing"), b"Reply with exactly this sentence and nothing else: then we bong"),
]


def newest(payload: bytes) -> str | None:
    """The newest thing the user said in a request body we know (the last message or input item when it's
    the user's, or claude.ai's prompt); None for any other body. Clients resend the whole conversation."""
    try:
        body = json.loads(payload)
    except ValueError:
        return None
    if not isinstance(body, dict):
        return None
    if isinstance(body.get("prompt"), str):
        return body["prompt"]
    if isinstance(body.get("messages"), list) and body["messages"]:
        return _user_text(body["messages"][-1])
    items = body.get("input")
    if isinstance(items, str):
        return items
    if isinstance(items, list):
        return _user_text(items[-1]) if items else ""
    return None


def _user_text(m) -> str:
    """A message's text when the user sent it: its content as a string, its text blocks (not tool results),
    or ChatGPT's content parts."""
    if not isinstance(m, dict) or "user" not in (m.get("role"), (m.get("author") or {}).get("role")):
        return ""
    c = m.get("content")
    if isinstance(c, str):
        return c
    if isinstance(c, list):
        return "\n".join(b.get("text", "") for b in c if isinstance(b, dict) and b.get("type") in ("text", "input_text"))
    if isinstance(c, dict):
        return "\n".join(p for p in c.get("parts") or [] if isinstance(p, str))
    return ""
TOKEN = os.environ.get("INTERCEPTOR_TOKEN", "")


class Interceptor(pb_grpc.InterceptorServicer):
    def _authorized(self, context) -> bool:
        if not TOKEN:
            return True
        md = dict(context.invocation_metadata())
        return md.get("authorization") == f"Bearer {TOKEN}"

    def OnCallStart(self, request, context):
        if not self._authorized(context):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "bad token")
        call = request.call
        logging.info("start %s %s %s%s model=%s", call.call_id, pb.Mode.Name(call.mode),
                     call.host, call.path, request.request.model)
        if request.request.model == "forbidden-model":
            return pb.Verdict(deny=pb.Deny(reason="forbidden-model isn't approved for use here."))
        return pb.Verdict()  # no action: Allow

    def OnMessage(self, request, context):
        if not self._authorized(context):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "bad token")
        payload, notes = request.payload, []
        if request.direction == pb.DIRECTION_CLIENT_TO_SERVER:
            said = newest(payload)
            said_bytes = payload if said is None else said.encode()
            for name, pattern, answer in ANSWERS:
                if pattern.search(said_bytes):
                    payload = pattern.sub(lambda _, a=answer: a, payload)
                    notes.append(pb.Annotation(key="answered", value=name))
        new, n = PHRASE.subn(b"[redacted]", payload)
        if n == 0 and not notes:
            return pb.Verdict()
        if n:
            notes.append(pb.Annotation(key="redacted", value=str(n)))
        return pb.Verdict(replace=pb.Replace(payload=new), annotations=notes)

    def OnCallComplete(self, request, context):
        r = request.response
        logging.info("complete %s status=%d model=%s in=%d out=%d verdicts=%d", request.call.call_id,
                     r.status, r.model, r.input_tokens, r.output_tokens, len(request.verdicts))
        return pb.Ack()


def main():
    logging.basicConfig(level=logging.INFO)
    # The observer sends request bodies up to 32 MiB; gRPC's default 4 MiB limit would fail a long session's.
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=32),
                         options=[("grpc.max_receive_message_length", 33 << 20)])
    pb_grpc.add_InterceptorServicer_to_server(Interceptor(), server)
    server.add_insecure_port(os.environ.get("LISTEN", "[::]:9100"))
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    main()
