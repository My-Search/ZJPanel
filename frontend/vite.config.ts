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
      // 插件前端资产（PluginFrame iframe 加载）：不代理会命中 vite SPA 回退，
      // iframe 里嵌回整个面板（应用套应用递归渲染）
      '/plugin-assets': { target: 'http://localhost:9000', changeOrigin: true },
    },
  },
})
