/// <reference types="vitest/config" />
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

declare const process: { env: Record<string, string | undefined> };

// Фронт движка (ADR-64): фронт по бланкам и панель админа, адрес /admin/. Фронт и ядро — один origin:
// /api проксируется (без CORS).
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  server: {
    port: 3000,
    proxy: {
      "/api": { target: process.env.CORE_URL ?? "http://localhost:8080", ws: true },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    include: ["tests/unit/**/*.test.{ts,tsx}"],
    setupFiles: ["tests/unit/setup.ts"],
  },
});
