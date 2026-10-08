package model

import "time"

type APIKey struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	KeyHash     string     `json:"-"`
	KeyPrefix   string     `json:"keyPrefix"`
	Permissions string     `json:"permissions"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	IsActive    bool       `json:"isActive"`
}

type GenerateKeyRequest struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	ExpiresDays int      `json:"expiresDays,omitempty"`
}

type GenerateKeyResponse struct {
	APIKey *APIKey `json:"apiKey"`
	RawKey string  `json:"rawKey"`
}

type RevokeKeyRequest struct {
	ID string `json:"id"`
}

const (
	PermTaskRead      = "task:read"
	PermTaskWrite     = "task:write"
	PermSettingsRead  = "settings:read"
	PermSettingsWrite = "settings:write"
	PermDiskRead      = "disk:read"
)

var AllPermissions = []string{
	PermTaskRead,
	PermTaskWrite,
	PermSettingsRead,
	PermSettingsWrite,
	PermDiskRead,
}

var PermissionNames = map[string]string{
	PermTaskRead:      "读取任务",
	PermTaskWrite:     "任务操作（启动/暂停/恢复/停止/重试/删除）",
	PermSettingsRead:  "读取设置",
	PermSettingsWrite: "修改设置",
	PermDiskRead:      "磁盘信息",
}

func (k *APIKey) HasPermission(perm string) bool {
	if k.Permissions == "*" {
		return true
	}
	for _, p := range splitPermissions(k.Permissions) {
		if p == perm {
			return true
		}
	}
	return false
}

func splitPermissions(s string) []string {
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
