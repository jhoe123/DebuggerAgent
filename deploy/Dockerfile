# Multi-stage build: build the React frontend, then the Go backend that serves it.
# Finalized in T9 (Cloud Run). The backend is expected to serve the built SPA from
# /app/web and expose the API on $PORT.

# --- frontend ---
FROM node:20-alpine AS frontend
WORKDIR /web
COPY frontend/package*.json ./
RUN npm ci || npm install
COPY frontend/ ./
RUN npm run build

# --- backend ---
FROM golang:1.24-alpine AS backend
WORKDIR /src
COPY backend/ ./backend/
WORKDIR /src/backend
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

# --- runtime ---
FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=backend /out/server /app/server
COPY --from=frontend /web/dist /app/web
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app/server"]
