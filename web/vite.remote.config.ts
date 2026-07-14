import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";
import { viteSingleFile } from "vite-plugin-singlefile";

export default defineConfig({
  root: resolve(__dirname, "remote"),
  cacheDir: resolve(__dirname, "node_modules/.vite-remote"),
  base: "/",
  plugins: [react(), viteSingleFile()],
  server: {
    port: 4201,
    strictPort: true,
    proxy: {
      "/api": "http://127.0.0.1:9201",
      "/docs": "http://127.0.0.1:9201",
      "/openapi.json": "http://127.0.0.1:9201"
    }
  },
  build: {
    outDir: resolve(__dirname, "dist/remote"),
    emptyOutDir: true,
    sourcemap: false,
    cssCodeSplit: false,
    assetsInlineLimit: 100000000
  }
});
