import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 构建产物输出到后端 embed 目录，由 Go 单二进制托管。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../backend/cmd/server/web/dist',
    emptyOutDir: true
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080'
    }
  }
})
