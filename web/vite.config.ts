import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://127.0.0.1:3000",
      "/auth": "http://127.0.0.1:3000",
      "/v1": "http://127.0.0.1:3000",
      "/images": "http://127.0.0.1:3000",
      "/healthz": "http://127.0.0.1:3000",
      "/version": "http://127.0.0.1:3000"
    }
  }
});
