import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { fileURLToPath, URL } from 'node:url';

// The SPA is served by the Go server from the same origin as the API, so
// there is no CORS and no second host. In development `pnpm dev` (and
// `pnpm preview`, over the built bundle) proxies the server's paths to
// FAPPS_DEV_BACKEND: the real server on :8080, or the mock (`pnpm mock`).
const backend = process.env.FAPPS_DEV_BACKEND ?? 'http://127.0.0.1:8080';
const proxy = {
  '/api': backend,
  '/v1': backend,
  '/auth': backend,
  '/healthz': backend,
};

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: { port: 5173, proxy },
  preview: { port: 4173, proxy },
  build: {
    outDir: 'dist',
    sourcemap: false,
    target: 'es2022',
  },
});
