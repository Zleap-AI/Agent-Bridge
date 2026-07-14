import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";
import { viteSingleFile } from "vite-plugin-singlefile";

export default defineConfig({
  root: resolve(__dirname, "local"),
  cacheDir: resolve(__dirname, "node_modules/.vite-local"),
  base: "/",
  plugins: [react(), viteSingleFile()],
  server: {
    port: 4202,
    strictPort: true,
    proxy: {
      "/api": "http://127.0.0.1:9202",
      "/agents": "http://127.0.0.1:9202",
      "/health": "http://127.0.0.1:9202",
      "/ws": { target: "ws://127.0.0.1:9202", ws: true }
    }
  },
  build: {
    outDir: resolve(__dirname, "dist/local"),
    emptyOutDir: true,
    sourcemap: false,
    cssCodeSplit: false,
    assetsInlineLimit: 100000000
  }
});
