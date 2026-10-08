package handler

import (
	"log"
	"net/http"

	"m3u8-downloader-web/storage"
)

func APIKeyMiddleware(storage *storage.SQLiteStorage, requiredPerm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := r.Header.Get("X-API-Key")
			if apiKey == "" {
				apiKey = r.Header.Get("X-Api-Key")
			}
			if apiKey == "" {
				apiKey = r.Header.Get("x-api-key")
			}
			// 兼容 Query 方式传递: ?api_key=xxx （方便调试/反代场景）
			if apiKey == "" {
				apiKey = r.URL.Query().Get("api_key")
			}

			if apiKey == "" {
				Err(w, http.StatusUnauthorized, "缺少 API Key，请在 X-API-Key Header 中提供")
				return
			}

			key, err := ValidateAPIKey(storage, apiKey)
			if err != nil {
				log.Printf("[APIKeyAuth] 验证失败: %v", err)
				Err(w, http.StatusUnauthorized, "API Key 无效: "+err.Error())
				return
			}

			log.Printf("[APIKeyAuth] 验证通过: KeyID=%s Name=%s", key.ID, key.Name)

			if requiredPerm != "" && !key.HasPermission(requiredPerm) {
				Err(w, http.StatusForbidden, "权限不足，需要: "+requiredPerm)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
