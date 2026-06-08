# PatchPilot — Cloud Run image.
# Runtime is Node 24 (so the Dynatrace MCP server runs) plus the Go server binary,
# the built React app, and the demo_app source (for the read_source tool).

# 1. Build the React frontend
FROM node:24-slim AS frontend
WORKDIR /web
COPY frontend/package*.json ./
RUN npm ci || npm install
COPY frontend/ ./
RUN npm run build

# 2. Build the Go backend
FROM golang:1.25 AS backend
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server

# 3. Runtime
FROM node:24-slim
WORKDIR /app
# ca-certificates: the static Go binary needs system root CAs to call Vertex AI over TLS.
# git: enables the optional Git source feature (clone/branch/commit/merge) on the host.
# Then pre-install the Dynatrace MCP server so the first request doesn't pay an npx download.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @dynatrace-oss/dynatrace-mcp-server@latest
COPY --from=backend /out/server /app/server
COPY --from=frontend /web/dist /app/web
COPY demo_app /app/demo_app
ENV PORT=8080 \
    WEB_DIR=/app/web \
    SOURCE_ROOT=/app/demo_app \
    PATCH_OUTPUT_DIR=/tmp/patches \
    GOOGLE_GENAI_USE_VERTEXAI=true \
    GOOGLE_CLOUD_LOCATION=global \
    DT_MCP_DISABLE_TELEMETRY=true \
    ENABLE_TEST_CONSOLE=true
# ^ Test Console defaults ON locally (backend git-resets/builds/runs source); pinned OFF
# here so the public hosted demo stays human-gated. Do not enable on a shared URL.
EXPOSE 8080
CMD ["/app/server"]
