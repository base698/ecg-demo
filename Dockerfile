FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/...

FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates curl && addgroup -S ecg && adduser -S -G ecg ecg
WORKDIR /app
USER ecg

# Standalone observation API; no demo router or promotion controls.
FROM runtime AS evidence
COPY --from=build /out/ecg /app/ecg
EXPOSE 9100
ENTRYPOINT ["/app/ecg"]
CMD ["-listen", "0.0.0.0:9100"]

# Self-contained mock example using the public libraries.
FROM runtime AS labels-example
COPY --from=build /out/labels-example /app/labels-example
ENTRYPOINT ["/app/labels-example"]

# Keep the original demo as the default build for deploy.sh and docker compose.
FROM runtime AS demo
COPY --from=build /out/controller /out/service /out/loadgen /app/
CMD ["/app/controller"]
