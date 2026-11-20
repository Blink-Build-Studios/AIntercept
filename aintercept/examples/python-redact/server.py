"""A minimal Interceptor in Python, on the raw protocol (aintercept/interceptor/v2/interceptor.proto,
docs/INTERCEPTOR.md): the Go example redact's behaviour. It replaces the clown's phrase with
"[redacted]" in the call's new messages on the way to the model (OnRequest) and in the model's reply text
on the way back (OnReply, holding back a tail that could be the start of the phrase, so a phrase split
across streamed pieces is still caught), answers a call for the model "forbidden-model" with a refusal
(OnCallStart), logs who makes each call and how it ended, and checks the bearer token the observer sends
when INTERCEPTOR_TOKEN is set. It doesn't implement OnToolCall, so the observer doesn't hold tool calls
for it."""
import json
import logging
import os
import re
from concurrent import futures

import grpc

from interceptor.v2 import interceptor_pb2 as pb
from interceptor.v2 import interceptor_pb2_grpc as pb_grpc

# The phrase, built from fragments so this file doesn't say it whole.
PHRASE = "sil" + "ly clo" + "wn"
PATTERN = re.compile(re.escape(PHRASE), re.IGNORECASE)
REDACTED = "[redacted]"
REFUSAL = "forbidden-model isn't approved for use here."
TOKEN = os.environ.get("INTERCEPTOR_TOKEN", "")


def log(msg, **fields):
    print(json.dumps({"msg": msg, **fields}), flush=True)


def held_tail(text: str) -> int:
    """How many bytes at the end of text could be the start of the phrase (0 when none)."""
    low = text.lower()
    for n in range(min(len(PHRASE) - 1, len(low)), 0, -1):
        if low.endswith(PHRASE[:n]):
            return n  # the phrase is ASCII: characters are bytes
    return 0


class Interceptor(pb_grpc.InterceptorServicer):
    def _check(self, context):
        if TOKEN and dict(context.invocation_metadata()).get("authorization") != f"Bearer {TOKEN}":
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "bad token")

    def OnCallStart(self, request, context):
        self._check(context)
        call, who = request.call, request.call.identity
        # verified: the provider confirmed the email for this call's credential; otherwise it's what the
        # client says.
        log("call start", call=call.call_id, mode="enforce" if call.mode == pb.MODE_ENFORCE else "observe", agent=call.client.product,
            model=request.request.model, email=who.email,
            verified=who.email_source == pb.IDENTITY_SOURCE_CREDENTIAL,
            account=who.verified_account_id or who.reported_account_id or who.org_id)
        if request.request.model == "forbidden-model":
            return pb.CallVerdict(respond=pb.Respond(text=REFUSAL))
        return pb.CallVerdict()  # no action: Accept

    def OnRequest(self, request, context):
        """Each new message with the phrase in a text part or a tool result comes back with it redacted.
        A part sent by reference (over the inline limit) stays as it is: this example doesn't Fetch."""
        self._check(context)
        verdict = pb.RequestVerdict()
        for message in request.messages:
            m = pb.ConvMessage()
            m.CopyFrom(message)  # every part comes back, by id, in order
            n = 0
            for part in m.parts:
                if part.ref:
                    continue
                if part.HasField("text"):
                    part.text.text, k = PATTERN.subn(REDACTED, part.text.text)
                    n += k
                elif part.HasField("tool_result"):
                    part.tool_result.text, k = PATTERN.subn(REDACTED, part.tool_result.text)
                    n += k
            if n:
                verdict.messages.append(pb.MessageVerdict(
                    message_id=m.id, replace=pb.Replace(message=m),
                    annotations=[pb.Annotation(key="redacted", value=str(n))]))
        return verdict

    def OnReply(self, request, context):
        """The reply's pending text, released with the phrase redacted; a tail that could still become the
        phrase is kept back for the next call (keep), until the block is done."""
        self._check(context)
        message = request.message
        if not message.parts or not message.parts[0].HasField("text"):
            return pb.MessageVerdict(message_id=message.id)  # an opaque part: the answer is ignored
        pending = message.parts[0].text.text
        keep = 0 if request.done else held_tail(pending)
        head = pending[:len(pending) - keep]
        new, n = PATTERN.subn(REDACTED, head)
        if not n:
            return pb.MessageVerdict(message_id=message.id, keep=keep)
        m = pb.ConvMessage()
        m.CopyFrom(message)
        m.parts[0].text.text = new
        return pb.MessageVerdict(message_id=message.id, replace=pb.Replace(message=m), keep=keep,
                                 annotations=[pb.Annotation(key="redacted", value=str(n))])

    def OnCallComplete(self, request, context):
        self._check(context)
        r = request.response
        log("call complete", call=request.call.call_id, ws_turn=request.ws_turn, status=r.status, model=r.model,
            input=r.input_tokens, output=r.output_tokens, answers=len(request.verdicts), replied=request.respond)
        return pb.Ack()


def main():
    logging.basicConfig(level=logging.INFO)
    # A fetched part can be up to 32 MiB: above gRPC's default 4 MiB receive limit.
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=32),
                         options=[("grpc.max_receive_message_length", 33 << 20)])
    pb_grpc.add_InterceptorServicer_to_server(Interceptor(), server)
    server.add_insecure_port(os.environ.get("LISTEN", "[::]:9100"))
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    main()
