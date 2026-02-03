import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    allowedHosts: ['localhost', 'gomoku-nginx', 'frontend'],
    hmr: {
      host: 'localhost',
      clientPort: 80,
      protocol: 'ws'
    }
  }
})
