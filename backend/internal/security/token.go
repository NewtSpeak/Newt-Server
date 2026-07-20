package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type Claims struct {
	jwt.RegisteredClaims
}

type TokenManager struct {
	secret    []byte
	accessTTL time.Duration
}

func NewTokenManager(secret string, accessTTL time.Duration) *TokenManager {
	return &TokenManager{secret: []byte(secret), accessTTL: accessTTL}
}

func (m *TokenManager) AccessToken(userID uuid.UUID) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(m.accessTTL)
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID.String(), Issuer: "owl-server", ID: uuid.NewString(),
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiresAt),
	}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	return token, expiresAt, err
}

func (m *TokenManager) ParseAccessToken(raw string) (uuid.UUID, error) {
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("无效签名算法")
		}
		return m.secret, nil
	}, jwt.WithIssuer("owl-server"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return uuid.Nil, errors.New("无效或已过期的访问令牌")
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return uuid.Nil, errors.New("无效访问令牌")
	}
	return uuid.Parse(claims.Subject)
}

func NewRefreshToken() (plain, hash string, err error) {
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(value)
	return plain, HashToken(plain), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
