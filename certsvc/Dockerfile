# Pinned Go toolchain for reproducible builds.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/certsvc ./cmd/certsvc

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/certsvc /certsvc
# Runs as nonroot (uid 65532) per the distroless nonroot tag.
ENTRYPOINT ["/certsvc"]
