package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	ctxKeyAdminAuthed = "admin_authed"
)

// AdminJWTMiddleware validates the Bearer JWT for admin endpoints.
// Requires the secret to be injected at server startup.
func AdminJWTMiddleware(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		if err := ParseAccessToken(jwtSecret, token); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}
		c.Set(ctxKeyAdminAuthed, true)
		c.Next()
	}
}

// ServiceBearerMiddleware validates the shared service secret for fetcher/checker.
func ServiceBearerMiddleware(serviceSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		if token != serviceSecret {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid service secret"})
			return
		}
		c.Next()
	}
}

// AdminOrServiceMiddleware accepts an administrator JWT or a service credential.
// Use it only for routes that are intentionally shared with service clients.
func AdminOrServiceMiddleware(jwtSecret, serviceSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		if token == serviceSecret {
			c.Set(ctxKeyAdminAuthed, true)
			c.Next()
			return
		}
		if err := ParseAccessToken(jwtSecret, token); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}
		c.Set(ctxKeyAdminAuthed, true)
		c.Next()
	}
}

// bearerToken extracts the raw token from "Authorization: Bearer <token>".
func bearerToken(c *gin.Context) (string, bool) {
	header := c.GetHeader("Authorization")
	if header == "" {
		return "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}
