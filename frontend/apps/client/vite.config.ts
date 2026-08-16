import { defineConfig } from "vite";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  base: "./",
  plugins: [tailwindcss()],
  build: { outDir: "../../../cmd/client/assets", emptyOutDir: false, sourcemap: true, target: "es2022" },
});
