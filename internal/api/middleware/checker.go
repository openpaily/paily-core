package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openpaily/paily-core/internal/config"
)

const checkerTagKey = "checker_tag"

// CheckerTagMiddleware authenticates checker submissions and injects the
// checker_tag into the gin context.
//
// Authentication priority:
//  1. Bearer token matches a cfg.Checkers entry → inject that entry's tag.
//  2. Bearer token matches cfg.Auth.ServiceSecret → tag = "default".
//  3. Neither matches → 401 Unauthorized.
func CheckerTagMiddleware(cfg *config.Config) gin.HandlerFunc {
	// Build secret→tag lookup once at startup.
	secretToTag := make(map[string]string, len(cfg.Checkers))
	for _, entry := range cfg.Checkers {
		if entry.Secret != "" {
			secretToTag[entry.Secret] = entry.Tag
		}
	}

	return func(c *gin.Context) {
		token := extractBearer(c.GetHeader("Authorization"))
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}

		// 1. Checker-specific secret.
		if tag, ok := secretToTag[token]; ok {
			c.Set(checkerTagKey, tag)
			c.Next()
			return
		}

		// 2. Fallback: service secret → "default".
		if cfg.Auth.ServiceSecret != "" && token == cfg.Auth.ServiceSecret {
			c.Set(checkerTagKey, "default")
			c.Next()
			return
		}

		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
	}
}

// CheckerTagFromCtx reads the checker tag injected by CheckerTagMiddleware.
// Returns "default" if the context value is absent or empty.
func CheckerTagFromCtx(c *gin.Context) string {
	tag, _ := c.Get(checkerTagKey)
	if s, ok := tag.(string); ok && s != "" {
		return s
	}
	return "default"
}

func extractBearer(authHeader string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(authHeader, prefix) {
		return strings.TrimPrefix(authHeader, prefix)
	}
	return ""
}
