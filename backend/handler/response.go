package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"m3u8-downloader-web/model"
)

// writeJSON 统一写 JSON 响应：设置 Content-Type、写状态码、序列化 body
func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// OK 写成功响应（Code=200, Message="success"）
func OK(w http.ResponseWriter, data interface{}) {
	writeJSON(w, http.StatusOK, model.APIResponse{
		Code:    200,
		Message: "success",
		Data:    data,
	})
}

// Err 写失败响应（Code=status）
func Err(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, model.APIResponse{
		Code:    status,
		Message: message,
	})
}

// requestOrigin 解析请求的节点源地址（scheme://host），用于写入 single 产物 meta.json。
// 反代场景优先 X-Forwarded-Proto；无 Host 时返回空串。
func requestOrigin(r *http.Request) string {
	if r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		// 多级代理取首个
		if idx := strings.IndexByte(proto, ','); idx >= 0 {
			proto = proto[:idx]
		}
		scheme = strings.TrimSpace(proto)
	}
	return scheme + "://" + r.Host
}
