package web

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed dist
var frontendAssets embed.FS

func RegisterFallback(router *gin.Engine, environment, devURL string) error {
	if _, err := fs.Stat(frontendAssets, "dist/index.html"); environment == "production" && err == nil {
		root, err := fs.Sub(frontendAssets, "dist")
		if err != nil {
			return err
		}
		files := http.FileServer(http.FS(root))
		router.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				c.JSON(404, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "接口不存在"}})
				return
			}
			requested := strings.TrimPrefix(filepath.Clean(c.Request.URL.Path), string(filepath.Separator))
			if info, err := fs.Stat(root, requested); err == nil && !info.IsDir() {
				files.ServeHTTP(c.Writer, c.Request)
				return
			}
			index, _ := fs.ReadFile(root, "index.html")
			c.Data(http.StatusOK, "text/html; charset=utf-8", index)
		})
		return nil
	}

	target, err := url.Parse(devURL)
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"error":{"code":"FRONTEND_UNAVAILABLE","message":"前端开发服务尚未启动"}}`))
	}
	router.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(404, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "接口不存在"}})
			return
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	})
	return nil
}
