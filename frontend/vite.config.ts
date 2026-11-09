import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// 构建产物直接输出到后端 embed 目录（go:embed all:dist）
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../backend/internal/http/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:9000', changeOrigin: true, ws: true },
      '/mcp': { target: 'http://localhost:9000', changeOrigin: true },
      '/_maintenance': { target: 'http://localhost:9000', changeOrigin: true },
    },
  },
})
