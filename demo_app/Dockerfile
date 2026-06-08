# Multi-stage image for the ShopFlow storefront (demo_app): build the React/TS frontend,
# build the Go binary, and serve both. Listens on :9090; deploy to Cloud Run with
# `--port 9090`. OTLP env (set at deploy time) makes it report to Dynatrace.

# 1) Build the storefront SPA -> web/dist
FROM node:24-slim AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2) Build the Go binary (static)
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/demo_app .

# 3) Runtime
FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/demo_app /app/demo_app
COPY --from=web /web/dist /app/web/dist
ENV DEMO_WEB_DIR=/app/web/dist
EXPOSE 9090
ENTRYPOINT ["/app/demo_app"]
