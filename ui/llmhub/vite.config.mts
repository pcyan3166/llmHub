import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";

export default defineConfig({
	root: fileURLToPath(new URL(".", import.meta.url)),
	plugins: [react()],
	build: { outDir: "dist", emptyOutDir: true },
	server: {
		host: "127.0.0.1",
		port: 3090,
		proxy: { "/api": "http://127.0.0.1:8090", "/health": "http://127.0.0.1:8090", "/ready": "http://127.0.0.1:8090" },
	},
});