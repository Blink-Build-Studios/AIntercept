# The end-to-end suite's v2 Interceptor (test/interceptor/compose.yml), built
# from the aintercept module. Not part of the product image.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/e2e-interceptor ./internal/e2e/interceptor

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/e2e-interceptor /e2e-interceptor
ENTRYPOINT ["/e2e-interceptor"]
