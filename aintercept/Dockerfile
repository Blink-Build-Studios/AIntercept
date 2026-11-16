# One image for every Go service: the container's args pick the mode
# (`observer`, `certsvc`). Pinned Go toolchain for reproducible builds.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/aintercept ./cmd/aintercept
# The example Interceptor (docs/INTERCEPTOR.md), for trying the control-plane
# hook: run the image with --entrypoint /redact-interceptor.
RUN CGO_ENABLED=0 go build -trimpath -o /out/redact-interceptor ./examples/redact
# The same on contract v2 (docs/INTERCEPTOR_V2.md): --entrypoint /rabbit-interceptor.
RUN CGO_ENABLED=0 go build -trimpath -o /out/rabbit-interceptor ./examples/rabbit

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aintercept /aintercept
COPY --from=build /out/redact-interceptor /redact-interceptor
COPY --from=build /out/rabbit-interceptor /rabbit-interceptor
# Runs as nonroot (uid 65532) per the distroless nonroot tag.
ENTRYPOINT ["/aintercept"]
