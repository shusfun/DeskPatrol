import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [tailwindcss(), react()],
  server: { proxy: { "/api": "http://127.0.0.1:18123", "/healthz": "http://127.0.0.1:18123" } },
  build: { outDir: "dist", sourcemap: true, target: "es2022" },
});
