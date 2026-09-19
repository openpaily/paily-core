package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	accessTokenDuration  = 15 * time.Minute
	refreshTokenDuration = 7 * 24 * time.Hour
)

type tokenType string

const (
	tokenTypeAccess  tokenType = "access"
	tokenTypeRefresh tokenType = "refresh"
)

type claims struct {
	TokenType tokenType `json:"token_type"`
	jwt.RegisteredClaims
}

// IssueAccessToken creates a signed JWT access token valid for 15 minutes.
func IssueAccessToken(secret string) (string, error) {
	return signToken(secret, tokenTypeAccess, accessTokenDuration)
}

// IssueRefreshToken creates a signed JWT refresh token valid for 7 days.
func IssueRefreshToken(secret string) (string, error) {
	return signToken(secret, tokenTypeRefresh, refreshTokenDuration)
}

func signToken(secret string, tt tokenType, dur time.Duration) (string, error) {
	now := time.Now()
	c := claims{
		TokenType: tt,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(dur)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return tok.SignedString([]byte(secret))
}

// ParseAccessToken validates a JWT and returns nil if it is a valid access token.
func ParseAccessToken(secret, tokenStr string) error {
	return parseToken(secret, tokenStr, tokenTypeAccess)
}

// ParseRefreshToken validates a JWT and returns nil if it is a valid refresh token.
func ParseRefreshToken(secret, tokenStr string) error {
	return parseToken(secret, tokenStr, tokenTypeRefresh)
}

func parseToken(secret, tokenStr string, expected tokenType) error {
	c := &claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return err
	}
	if !tok.Valid {
		return errors.New("invalid token")
	}
	if c.TokenType != expected {
		return fmt.Errorf("wrong token type: got %q, want %q", c.TokenType, expected)
	}
	return nil
}
