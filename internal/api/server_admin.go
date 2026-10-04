package api

import (
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/admin"
)

func (s *Server) setupAdminRoutes() {
	if !s.cfg.RemoteManagement.Admin.Enabled {
		return
	}
	available := func(c *gin.Context) {
		cfg := s.getConfig()
		if cfg == nil || cfg.Home.Enabled {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if s.admin == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "administrator_unavailable"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
	s.engine.GET("/admin/*path", available, admin.ServeAsset)
	s.engine.HEAD("/admin/*path", available, admin.ServeAsset)
	group := s.engine.Group("/v8/management/auth", available, func(c *gin.Context) {
		if !s.adminRemoteAccessAllowed(c) {
			return
		}
		c.Next()
	})
	group.POST("/login", func(c *gin.Context) { s.admin.Login(c) })
	group.GET("/session", func(c *gin.Context) { s.admin.Session(c) })
	group.POST("/logout", func(c *gin.Context) { s.admin.Logout(c) })
	group.PUT("/password", func(c *gin.Context) { s.admin.ChangePassword(c) })
}

func (s *Server) adminRemoteAccessAllowed(c *gin.Context) bool {
	cfg := s.getConfig()
	ip := net.ParseIP(c.ClientIP())
	if cfg != nil && (cfg.RemoteManagement.AllowRemote || (ip != nil && ip.IsLoopback())) {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "remote management disabled"})
	return false
}

func (s *Server) managementV8AvailabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := s.getConfig()
		if cfg == nil || cfg.Home.Enabled {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if s.admin == nil && !s.managementAvailable(c) {
			return
		}
		c.Next()
	}
}

func (s *Server) managementV8AuthMiddleware() gin.HandlerFunc {
	legacy := s.mgmt.Middleware()
	return func(c *gin.Context) {
		if s.admin != nil && c.GetHeader("Authorization") == "" && c.GetHeader("X-Management-Key") == "" {
			if !s.adminRemoteAccessAllowed(c) || !s.admin.Authorize(c) {
				return
			}
			c.Next()
			return
		}
		legacy(c)
	}
}
