import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Dev: proxy the local API to a running `parley console --no-open --listen 127.0.0.1:8479`.
export default defineConfig({
  plugins: [react()],
  server: { port: 5174, proxy: { '/v1': { target: process.env.PARLEY_API || 'http://127.0.0.1:8479', changeOrigin: true } } },
})
