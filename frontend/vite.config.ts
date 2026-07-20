import { reactRouter } from "@react-router/dev/vite"
import tailwindcss from "@tailwindcss/vite"
import { defineConfig } from "vite"

export default defineConfig({
  resolve: { tsconfigPaths: true },
  plugins: [tailwindcss(), reactRouter()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
      "/gapi": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
      // 公开静态资产（用户头像 / 服务器图标 / banner）：后端返回的
      // /public-assets/... 相对路径需在 dev 下代理到后端才能同源加载。
      "/public-assets": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
      "/healthz": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
      "/swagger": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
    },
  },
})
