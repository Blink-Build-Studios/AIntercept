"""A minimal Interceptor in Python (docs/BUILDING_AN_INTERCEPTOR.md): redacts
"silly clown" in both directions, refuses the model "forbidden-model", and
checks the bearer token the observer sends when INTERCEPTOR_TOKEN is set."""
import logging
import os
import re
from concurrent import futures

import grpc

from interceptor.v1 import interceptor_pb2 as pb
from interceptor.v1 import interceptor_pb2_grpc as pb_grpc

PHRASE = re.compile(rb"(?i)silly clown")
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
        new, n = PHRASE.subn(b"[redacted]", request.payload)
        if n == 0:
            return pb.Verdict()
        return pb.Verdict(replace=pb.Replace(payload=new),
                          annotations=[pb.Annotation(key="redacted", value=str(n))])

    def OnCallComplete(self, request, context):
        r = request.response
        logging.info("complete %s status=%d model=%s in=%d out=%d verdicts=%d", request.call.call_id,
                     r.status, r.model, r.input_tokens, r.output_tokens, len(request.verdicts))
        return pb.Ack()


def main():
    logging.basicConfig(level=logging.INFO)
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=32))
    pb_grpc.add_InterceptorServicer_to_server(Interceptor(), server)
    server.add_insecure_port(os.environ.get("LISTEN", "[::]:9100"))
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    main()
