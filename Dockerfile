FROM golang:1.25.13-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hl-mcp ./cmd/hl-mcp \
    && mkdir -p /out/data \
    && chown -R 65532:65532 /out/data

# Fly mounts a new volume as root. The binary only fixes the SQLite path,
# clears supplementary groups, and permanently drops to uid/gid 65532 before
# opening the database, creating HTTP clients, or listening on a socket.
FROM gcr.io/distroless/static-debian12
ENV DB_PATH=/data/hl-mcp.db
ENV GOMEMLIMIT=400MiB
COPY --from=builder --chown=65532:65532 /out/hl-mcp /hl-mcp
COPY --from=builder --chown=65532:65532 /out/data /data
USER 0:0
EXPOSE 3000
ENTRYPOINT ["/hl-mcp"]
