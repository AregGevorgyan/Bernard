import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The portal is served by the Go binary in production; during `npm run dev`
// the API calls are proxied so the two halves can run side by side.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/media": "http://localhost:8080",
    },
  },
});
