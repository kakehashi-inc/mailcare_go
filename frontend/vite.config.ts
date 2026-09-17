import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The dev server proxies the API and login endpoints to a locally running
// MailCare server (default port 9790) so cookies work on the same origin.
const BACKEND = process.env.MAILCARE_BACKEND ?? "http://localhost:9790";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/api": BACKEND,
      "/web": BACKEND,
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
