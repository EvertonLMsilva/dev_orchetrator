FROM golang:1.25 AS validation

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN chmod +x ./scripts/validate.sh

FROM validation AS build
RUN CGO_ENABLED=0 go build -trimpath -o /usr/local/bin/orchestrator ./cmd/orchestrator

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates
COPY --from=build /usr/local/bin/orchestrator /usr/local/bin/orchestrator
WORKDIR /
ENTRYPOINT ["/usr/local/bin/orchestrator"]

# Preserve the existing default validation image; deployment selects runtime.
FROM validation AS default
