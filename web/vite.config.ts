import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { outDir: "dist", emptyOutDir: true, assetsDir: "assets", assetsInlineLimit: 0 },
  server: { proxy: { "/v1": "http://127.0.0.1:13003" } },
});
