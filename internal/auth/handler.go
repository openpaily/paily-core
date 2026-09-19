package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/openpaily/paily-core/internal/api/response"
	"golang.org/x/crypto/bcrypt"
)

// Handler handles auth-related HTTP endpoints.
type Handler struct {
	adminPassword string // plaintext; compared via bcrypt.CompareHashAndPassword
	jwtSecret     string
}

// NewHandler creates an auth handler.
// adminPassword is the plaintext password from config; it is never stored in DB.
func NewHandler(adminPassword, jwtSecret string) *Handler {
	return &Handler{adminPassword: adminPassword, jwtSecret: jwtSecret}
}

type loginRequest struct {
	Password string `json:"password" binding:"required"`
}

type loginResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

// Login handles POST /api/v1/auth/login.
func (h *Handler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	// Support both plaintext comparison and bcrypt hash stored in config.
	if err := bcrypt.CompareHashAndPassword([]byte(h.adminPassword), []byte(req.Password)); err != nil {
		// Fallback: plaintext comparison (for dev convenience; production should use bcrypt hash)
		if req.Password != h.adminPassword {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
			return
		}
	}

	accessToken, err := IssueAccessToken(h.jwtSecret)
	if err != nil {
		response.InternalError(c, "failed to issue token")
		return
	}
	refreshToken, err := IssueRefreshToken(h.jwtSecret)
	if err != nil {
		response.InternalError(c, "failed to issue refresh token")
		return
	}

	c.JSON(http.StatusOK, loginResponse{Token: accessToken, RefreshToken: refreshToken})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Refresh handles POST /api/v1/auth/refresh.
func (h *Handler) Refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	if err := ParseRefreshToken(h.jwtSecret, req.RefreshToken); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
		return
	}

	accessToken, err := IssueAccessToken(h.jwtSecret)
	if err != nil {
		response.InternalError(c, "failed to issue token")
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": accessToken})
}
