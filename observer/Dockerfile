FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/observer ./cmd/observer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/observer /observer
EXPOSE 9000 9001
ENTRYPOINT ["/observer"]
