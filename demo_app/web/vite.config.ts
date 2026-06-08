import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Go server (demo_app, :9090) serves the built SPA from web/dist. In dev, Vite runs
// on 5174 (the agent's frontend uses 5173) and proxies the storefront's API calls plus
// the two legacy bare routes to the Go server. base "./" keeps built /assets references
// relative so they resolve when served from Go's root.
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: { outDir: "dist" },
  server: {
    port: 5174,
    proxy: {
      "/api": "http://localhost:9090",
      "/checkout": "http://localhost:9090",
      "/report": "http://localhost:9090",
    },
  },
});
