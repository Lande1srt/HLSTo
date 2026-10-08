package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/storage"
)

type APIKeyHandler struct {
	storage *storage.SQLiteStorage
}

func NewAPIKeyHandler(storage *storage.SQLiteStorage) *APIKeyHandler {
	return &APIKeyHandler{storage: storage}
}

func (h *APIKeyHandler) GenerateKey(w http.ResponseWriter, r *http.Request) {
	var req model.GenerateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if req.Name == "" {
		Err(w, http.StatusBadRequest, "密钥名称不能为空")
		return
	}

	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		Err(w, http.StatusInternalServerError, "生成密钥失败")
		return
	}

	rawKeyStr := base64.RawURLEncoding.EncodeToString(rawKey)

	hash := sha256.Sum256([]byte(rawKeyStr))
	keyHash := hex.EncodeToString(hash[:])

	permissions := "*"
	if len(req.Permissions) > 0 {
		permissionsStr := ""
		for i, p := range req.Permissions {
			if i > 0 {
				permissionsStr += ","
			}
			permissionsStr += p
		}
		permissions = permissionsStr
	}

	keyID := hex.EncodeToString(rawKey[:8])

	var expiresAt *time.Time
	if req.ExpiresDays > 0 {
		t := time.Now().AddDate(0, 0, req.ExpiresDays)
		expiresAt = &t
	}

	apiKey := &model.APIKey{
		ID:          keyID,
		Name:        req.Name,
		KeyHash:     keyHash,
		KeyPrefix:   rawKeyStr[:8],
		Permissions: permissions,
		CreatedAt:   time.Now(),
		ExpiresAt:   expiresAt,
		IsActive:    true,
	}

	if err := h.storage.AddAPIKey(apiKey); err != nil {
		log.Printf("[APIKey] Error saving key: %v", err)
		Err(w, http.StatusInternalServerError, "保存密钥失败")
		return
	}

	OK(w, model.GenerateKeyResponse{
		APIKey: apiKey,
		RawKey: rawKeyStr,
	})
}

func (h *APIKeyHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.storage.ListAPIKeys()
	if err != nil {
		Err(w, http.StatusInternalServerError, "获取密钥列表失败")
		return
	}

	type KeyInfo struct {
		ID          string     `json:"id"`
		Name        string     `json:"name"`
		KeyPrefix   string     `json:"keyPrefix"`
		Permissions []string   `json:"permissions"`
		CreatedAt   time.Time  `json:"createdAt"`
		LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
		ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
		IsActive    bool       `json:"isActive"`
	}

	result := make([]KeyInfo, 0)
	for _, k := range keys {
		perms := parsePermissions(k.Permissions)
		if k.Permissions == "*" {
			perms = model.AllPermissions
		}
		result = append(result, KeyInfo{
			ID:          k.ID,
			Name:        k.Name,
			KeyPrefix:   k.KeyPrefix,
			Permissions: perms,
			CreatedAt:   k.CreatedAt,
			LastUsedAt:  k.LastUsedAt,
			ExpiresAt:   k.ExpiresAt,
			IsActive:    k.IsActive,
		})
	}

	OK(w, result)
}

func (h *APIKeyHandler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	var req model.RevokeKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if req.ID == "" {
		Err(w, http.StatusBadRequest, "密钥ID不能为空")
		return
	}

	if err := h.storage.RevokeAPIKey(req.ID); err != nil {
		Err(w, http.StatusInternalServerError, "撤销密钥失败")
		return
	}

	OK(w, map[string]string{"message": "密钥已撤销"})
}

func (h *APIKeyHandler) DeleteKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if req.ID == "" {
		Err(w, http.StatusBadRequest, "密钥ID不能为空")
		return
	}

	if err := h.storage.DeleteAPIKey(req.ID); err != nil {
		Err(w, http.StatusInternalServerError, "删除密钥失败")
		return
	}

	OK(w, map[string]string{"message": "密钥已删除"})
}

func parsePermissions(s string) []string {
	var result []string
	current := ""
	for _, c := range s {
		if c == ',' {
			if current != "" {
				result = append(result, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func ValidateAPIKey(storage *storage.SQLiteStorage, apiKey string) (*model.APIKey, error) {
	trimmedKey := strings.TrimSpace(apiKey)
	if trimmedKey != apiKey {
		log.Printf("[ValidateAPIKey] 警告: 传入的 API Key 含有前后空白字符，已自动去除 (原长度=%d, 去除后=%d)", len(apiKey), len(trimmedKey))
	}

	hash := sha256.Sum256([]byte(trimmedKey))
	keyHash := hex.EncodeToString(hash[:])

	prefix := ""
	if len(trimmedKey) >= 8 {
		prefix = trimmedKey[:8]
	} else if len(trimmedKey) > 0 {
		prefix = trimmedKey
	}

	log.Printf("[ValidateAPIKey] Key前缀: %s..., Key长度: %d, 计算哈希: %s",
		prefix, len(trimmedKey), keyHash)

	key, err := storage.GetAPIKeyByHash(keyHash)
	if err != nil {
		log.Printf("[ValidateAPIKey] 数据库查询失败: %v", err)
		return nil, fmt.Errorf("验证密钥失败: %w", err)
	}
	if key == nil {
		// 尝试列出所有Key的前缀，帮助调试
		allKeys, listErr := storage.ListAPIKeys()
		if listErr == nil && len(allKeys) > 0 {
			log.Printf("[ValidateAPIKey] 密钥无效！数据库中现有 %d 个密钥的前缀: ", len(allKeys))
			for _, k := range allKeys {
				log.Printf("[ValidateAPIKey]   - ID=%s Name=%s Prefix=%s IsActive=%v Hash=%s",
					k.ID, k.Name, k.KeyPrefix, k.IsActive, k.KeyHash[:16]+"...")
			}
		} else if listErr != nil {
			log.Printf("[ValidateAPIKey] 列出密钥列表失败: %v", listErr)
		} else {
			log.Printf("[ValidateAPIKey] 密钥无效！数据库中没有任何API Key记录")
		}
		return nil, fmt.Errorf("密钥无效")
	}

	log.Printf("[ValidateAPIKey] 找到匹配密钥: ID=%s Name=%s Prefix=%s IsActive=%v Permissions=%s",
		key.ID, key.Name, key.KeyPrefix, key.IsActive, key.Permissions)

	if !key.IsActive {
		log.Printf("[ValidateAPIKey] 密钥已被撤销: ID=%s", key.ID)
		return nil, fmt.Errorf("密钥已被撤销")
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		log.Printf("[ValidateAPIKey] 密钥已过期: ID=%s ExpiresAt=%v", key.ID, key.ExpiresAt)
		return nil, fmt.Errorf("密钥已过期")
	}

	_ = storage.UpdateAPIKeyLastUsed(key.ID)

	return key, nil
}
