import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// O proxy de desenvolvimento mantém os mesmos caminhos /api usados no container.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        rewrite: (path) => path.replace(/^\/api/, ""),
      },
    },
  },
});
