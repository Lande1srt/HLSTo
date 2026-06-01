package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"m3u8-downloader-web/model"
)

var (
	JWT_SECRET []byte
	USERNAME   string
	PASSWORD   string
)

type AuthHandler struct{}

func NewAuthHandler() *AuthHandler {
	InitAuth()
	return &AuthHandler{}
}

func InitAuth() {
	// 系统环境变量优先级高于 .env
	// 如果已经在运行前通过 export/set 设置了环境变量，它们会由操作系统直接加载
	// 此处逻辑保持不变，因为 os.Getenv 会自动获取最新的环境变量（包括 godotenv 注入的）
	// godotenv.Load() 默认不会覆盖已存在的环境变量，这已经符合“系统环境变量优先级更高”的原则
	USERNAME = os.Getenv("AUTH_USERNAME")
	PASSWORD = os.Getenv("AUTH_PASSWORD")
	
	secret := os.Getenv("JWT_SECRET")
	if secret != "" {
		JWT_SECRET = []byte(secret)
	} else if len(JWT_SECRET) == 0 {
		JWT_SECRET = []byte("m3u8-downloader-secret-key")
	}
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if !isAuthEnabled() {
		h.sendSuccess(w, map[string]string{"message": "Authentication is disabled"})
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if req.Username == USERNAME && req.Password == PASSWORD {
		token := createToken(req.Username)
		h.sendSuccess(w, LoginResponse{Token: token})
	} else {
		h.sendError(w, http.StatusUnauthorized, "用户名或密码错误")
	}
}

func (h *AuthHandler) CheckAuth(w http.ResponseWriter, r *http.Request) {
	if !isAuthEnabled() {
		h.sendSuccess(w, map[string]bool{"authenticated": true, "authEnabled": false})
		return
	}

	token := extractToken(r)
	if token == "" {
		h.sendSuccess(w, map[string]bool{"authenticated": false, "authEnabled": true})
		return
	}

	_, err := validateToken(token)
	if err != nil {
		h.sendSuccess(w, map[string]bool{"authenticated": false, "authEnabled": true})
		return
	}

	h.sendSuccess(w, map[string]bool{"authenticated": true, "authEnabled": true})
}

func (h *AuthHandler) sendSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    200,
		Message: "success",
		Data:    data,
	})
}

func (h *AuthHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    status,
		Message: message,
	})
}

func isAuthEnabled() bool {
	return USERNAME != "" && PASSWORD != ""
}

func createToken(username string) string {
	claims := jwt.MapClaims{
		"username": username,
		"exp":      time.Now().Add(time.Hour * 24).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString(JWT_SECRET)
	return tokenString
}

func validateToken(tokenString string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return JWT_SECRET, nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, err
}

func extractToken(r *http.Request) string {
	token := r.Header.Get("Authorization")
	if len(token) > 7 && token[:7] == "Bearer " {
		return token[7:]
	}
	return ""
}

func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAuthEnabled() {
			next.ServeHTTP(w, r)
			return
		}

		token := extractToken(r)
		if token == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(model.APIResponse{
				Code:    http.StatusUnauthorized,
				Message: "未授权访问，请先登录",
			})
			return
		}

		_, err := validateToken(token)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(model.APIResponse{
				Code:    http.StatusUnauthorized,
				Message: "Token 无效或已过期",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}
