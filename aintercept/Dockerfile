# One image for every Go service: the container's args pick the mode
# (`observer`, `certsvc`). Pinned Go toolchain for reproducible builds.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Production images build with no tags, which compiles the dev body-capture
# paths out entirely. Local compose passes GO_TAGS=devcapture.
ARG GO_TAGS=""
RUN CGO_ENABLED=0 go build -trimpath -tags "$GO_TAGS" -o /out/aintercept ./cmd/aintercept

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aintercept /aintercept
# Runs as nonroot (uid 65532) per the distroless nonroot tag.
ENTRYPOINT ["/aintercept"]
