package admin

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed static/index.html static/admin.css static/admin.js static/theme.js static/oil.css
var assets embed.FS

func ServeAsset(c *gin.Context) {
	path := c.Param("path")
	var name, contentType string
	switch path {
	case "/", "", "/index.html":
		name, contentType = "index.html", "text/html; charset=utf-8"
	case "/admin.css":
		name, contentType = "admin.css", "text/css; charset=utf-8"
	case "/oil.css":
		name, contentType = "oil.css", "text/css; charset=utf-8"
	case "/admin.js":
		name, contentType = "admin.js", "text/javascript; charset=utf-8"
	case "/theme.js":
		name, contentType = "theme.js", "text/javascript; charset=utf-8"
	default:
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	data, errRead := assets.ReadFile("static/" + name)
	if errRead != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusOK, contentType, data)
}
