import axios from 'axios'
import type { Settings } from '@/stores/settings'

const api = axios.create({
  baseURL: '/api',
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json'
  }
})

api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

api.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('token')
    }
    return Promise.reject(error)
  }
)

export const authAPI = {
  login: (credentials: Record<string, string>) => api.post('/auth/login', credentials),
  check: () => api.get('/auth/check')
}

export const downloadAPI = {
  start: (data: {
    url: string
    threadCount?: number
    outputName?: string
    hostType?: string
    cookie?: string
    autoClear?: boolean
    savePath?: string
  }) => api.post('/download/start', data),

  stop: (taskId: string) => api.post('/download/stop', { taskId }),

  pause: (taskId: string) => api.post('/download/pause', { taskId }),

  resume: (taskId: string) => api.post('/download/resume', { taskId }),

  retry: (taskId: string, mode?: string) => api.post('/download/retry', { taskId, mode }),

  upload: (taskId: string, config?: Record<string, unknown>) => api.post('/download/upload', { taskId, config }),

  analyze: (url: string, referer?: string, cookie?: string) => api.post('/download/analyze', { url, referer, cookie })
}

// saveBlob 触发浏览器下载一个 Blob
export const saveBlob = (blob: Blob, filename: string) => {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
}

export const taskAPI = {
  list: () => api.get('/tasks'),

  get: (id: string) => api.get(`/tasks/${id}`),

  delete: (id: string) => api.delete(`/tasks/${id}`),

  // 下载任务关联的 HLS 加密密钥
  downloadKey: (id: string) =>
    api.get(`/tasks/${id}/key`, { responseType: 'blob', timeout: 60000 }),

  // 查询任务持久化日志（level 空串为全部）
  getLogs: (id: string, level = '', limit = 5000) =>
    api.get(`/tasks/${id}/logs`, { params: { level: level || undefined, limit } }),

  // 导出任务日志为 .log 文件
  downloadLogs: (id: string) =>
    api.get(`/tasks/${id}/logs/download`, { responseType: 'blob', timeout: 120000 })
}

export const settingsAPI = {
  get: () => api.get('/settings'),

  save: (settings: Settings) => api.post('/settings', settings),

  testWebDAV: (config: Settings) => api.post('/settings/webdav/test', config),

  listWebDAVDir: (data: { url: string; username?: string; password?: string; path: string }) =>
    api.post('/settings/webdav/list', data),

  clearCache: () => api.post('/settings/clear-cache'),

  getCleanupConfig: () => api.get('/settings/cleanup-config'),

  saveCleanupConfig: (config: { enabled: boolean; interval: number; unit: string }) =>
    api.post('/settings/cleanup-config', config),

  startSpeedTest: (data: {
    webDAVURL: string
    webDAVUsername?: string
    webDAVPassword?: string
    webDAVRemoteDir?: string
    sizeMB?: number
  }) => api.post('/settings/speedtest/start', data),

  stopSpeedTest: () => api.post('/settings/speedtest/stop'),

  getSpeedTestLog: () => api.get('/settings/speedtest')
}

export const diskAPI = {
  getInfo: () => api.get('/disk/info'),
  getAllDisks: () => api.get('/disk/all'),
  checkSpace: (path?: string) => api.post('/disk/check-space', { path })
}

export interface FFmpegStatus {
  available: boolean
  source: string // env / system / private
  path: string
  version: string
  privateDir: string
  supported: boolean
  installing: boolean
  progress: number
  phase: string // idle / downloading / extracting / verifying
  message: string
  lastError?: string
}

export const ffmpegAPI = {
  status: (forceRefresh = false) => api.get('/ffmpeg/status', { params: { forceRefresh: forceRefresh ? 1 : 0 } }),
  install: () => api.post('/ffmpeg/install')
}

export interface APIKeyInfo {
  id: string
  name: string
  keyPrefix: string
  permissions: string[]
  createdAt: string
  lastUsedAt?: string
  expiresAt?: string
  isActive: boolean
}

export const apiKeyAPI = {
  generate: (data: { name: string; permissions: string[]; expiresDays?: number }) =>
    api.post('/apikey/generate', data),
  list: () => api.post('/apikey/list'),
  revoke: (id: string) => api.post('/apikey/revoke', { id }),
  delete: (id: string) => api.post('/apikey/delete', { id })
}

export default api
