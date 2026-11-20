# One image for every Go service: the container's args pick the mode
# (`observer`, `certsvc`). Pinned Go toolchain for reproducible builds. Go
# cross-compiles, so one build makes every platform's image natively
# (`docker buildx build --platform linux/amd64,linux/arm64`).
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/aintercept ./cmd/aintercept
# The example Interceptors (docs/INTERCEPTOR.md), for trying the control-plane
# hook: run the image with --entrypoint /redact-interceptor, or
# /rabbit-interceptor.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/redact-interceptor ./examples/redact
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/rabbit-interceptor ./examples/rabbit

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aintercept /aintercept
COPY --from=build /out/redact-interceptor /redact-interceptor
COPY --from=build /out/rabbit-interceptor /rabbit-interceptor
# Runs as nonroot (uid 65532) per the distroless nonroot tag.
ENTRYPOINT ["/aintercept"]
