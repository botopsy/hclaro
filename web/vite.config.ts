import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
export default defineConfig({
  plugins: [react(), tailwindcss(), {
    name: 'local-development-boundary',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (req.headers.host !== '127.0.0.1:5173' || req.headers.origin && req.headers.origin !== 'http://127.0.0.1:5173' || req.headers['sec-fetch-site'] === 'cross-site') {
          res.statusCode = 403;
          res.setHeader('Content-Type', 'application/json');
          res.end(JSON.stringify({ error: 'forbidden_origin', reason: 'This request did not come from the Hclaro page on this address.' }));
          return;
        }
        if (req.method !== 'GET' && req.method !== 'HEAD') {
          res.statusCode = 405;
          res.setHeader('Content-Type', 'application/json');
          res.end(JSON.stringify({ error: 'method_not_allowed', reason: 'The development server is read-only.' }));
          return;
        }
        next();
      });
    }
  }],
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    cors: false,
    proxy: {
      '/api/': {
        target: 'http://127.0.0.1:9273',
        changeOrigin: true,
        configure(proxy) {
          // Only the validated Vite origin reaches this same-machine development proxy.
          proxy.on('proxyReq', request => request.setHeader('Origin', 'http://127.0.0.1:9273'));
        }
      }
    }
  },
  build: {
    outDir: '../internal/ui/dist',
    emptyOutDir: true,
    sourcemap: false,
    // The local CSP allows font-src/img-src 'self' only for fonts; never inline them as data: URIs.
    assetsInlineLimit: 0
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test-setup.ts',
    globals: true
  }
});
