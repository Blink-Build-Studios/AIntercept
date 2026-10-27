FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod ./
# Fixture has no external deps; go.sum may be absent.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/fixture ./cmd/fixture

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fixture /fixture
EXPOSE 8443
ENTRYPOINT ["/fixture"]
