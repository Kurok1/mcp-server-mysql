/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { viteSingleFile } from "vite-plugin-singlefile";

export default defineConfig({
  build: {
    cssCodeSplit: false,
    outDir: "dist/client",
  },
  optimizeDeps: {
    include: ["react", "react-dom/client"],
  },
  server: {
    host: "0.0.0.0",
    allowedHosts: ["terminal.local"],
    warmup: {
      clientFiles: ["./src/main.tsx"],
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["./vitest.setup.ts"],
  },
  plugins: [
    react(),
    {
      name: "strip-source-file-header",
      enforce: "post",
      transformIndexHtml(html) {
        return html.replace(/^<!--\s*@author[\s\S]*?@since[\s\S]*?-->\s*/, "");
      },
    },
    viteSingleFile(),
  ],
});
