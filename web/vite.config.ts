import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const target = 'http://localhost:8080'
const proxy = {
  target,
  changeOrigin: true,
  headers: { Origin: target },
}

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/health': proxy,
      '/ready': proxy,
      '/v1': proxy,
      '/api': proxy,
    },
  },
})
