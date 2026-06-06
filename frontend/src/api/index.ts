import axios from 'axios'
import type { Settings } from '@/stores/settings'

const api = axios.create({
  baseURL: '/api',
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json'
  }
})

// 添加请求拦截器，注入 Token
api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 添加响应拦截器，处理 401
api.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('token')
      // 如果不是登录页，可以考虑跳转，但通常由路由守卫处理
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

  retry: (taskId: string) => api.post('/download/retry', { taskId }),

  upload: (taskId: string, config?: Record<string, unknown>) => api.post('/download/upload', { taskId, config }),

  analyze: (url: string, referer?: string, cookie?: string) => api.post('/download/analyze', { url, referer, cookie })
}

export const taskAPI = {
  list: () => api.get('/tasks'),

  get: (id: string) => api.get(`/tasks/${id}`),

  delete: (id: string) => api.delete(`/tasks/${id}`)
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
    api.post('/settings/cleanup-config', config)
}

export default api
