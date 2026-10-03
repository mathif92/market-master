import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

const GATEWAY = process.env.VITE_GATEWAY_URL ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    // allow demo.localhost:5173 (host-based market resolution in dev)
    allowedHosts: ['.localhost', 'localhost'],
    proxy: {
      '/v1': {
        target: GATEWAY,
        changeOrigin: true,
      },
    },
  },
  preview: {
    port: 5173,
    proxy: {
      '/v1': { target: GATEWAY, changeOrigin: true },
    },
  },
})
