import { defineStore } from 'pinia'
import { ref } from 'vue'
import { downloadAPI, taskAPI } from '@/api'
import { buildWsUrl } from '@/api/runtime'

export interface Task {
  id: string
  url: string
  name: string
  status: 'pending' | 'downloading' | 'merging' | 'compressing' | 'packing' | 'uploading' | 'completed' | 'failed' | 'paused'
  progress: number
  speed: string
  totalSegments: number
  downloadedSegments: number
  // 通用视频（非 m3u8）使用真实字节数；m3u8 任务这两个字段为 0
  totalBytes?: number
  downloadedBytes?: number
  outputPath?: string
  keyPath?: string
  error?: string
  referer?: string
  createdAt: string
  completedAt?: string
  webDAVURL?: string
  webDAVUsername?: string
  webDAVPassword?: string
  webDAVRemoteDir?: string
  deleteAfterUpload?: boolean
}

export interface LogEntry {
  level: 'info' | 'debug' | 'warn' | 'error'
  message: string
  timestamp: string
}

export const useDownloadStore = defineStore('download', () => {
  const currentTask = ref<Task | null>(null)
  const logs = ref<LogEntry[]>([])
  const isDownloading = ref(false)
  const ws = ref<WebSocket | null>(null)

  // ===== WS 连接生命周期管理（自动重连 + REST 兜底）=====
  // 桌面端（Wails/WebView2）带 taskId 的 WS 可能因 PNA/网络抖动关闭，
  // 原先无重连、无兜底会导致首页永久丢失状态。
  let activeTaskId: string | null = null
  let wsManualClosed = false
  let wsRetryTimer: ReturnType<typeof setTimeout> | null = null
  let wsRetryAttempts = 0
  let pollTimer: ReturnType<typeof setInterval> | null = null
  let polling = false

  const ACTIVE: Task['status'][] = ['pending', 'downloading', 'merging', 'compressing', 'packing', 'uploading', 'paused']

  const clearRetryTimer = () => {
    if (wsRetryTimer !== null) {
      clearTimeout(wsRetryTimer)
      wsRetryTimer = null
    }
  }

  // stopPolling 停止 REST 兜底轮询
  const stopPolling = () => {
    if (pollTimer !== null) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  // syncTaskFromServer 通过 REST 拉取单任务状态做最终一致兜底
  const syncTaskFromServer = async (taskId: string) => {
    if (polling) return
    polling = true
    try {
      const res = await taskAPI.get(taskId)
      const t = res.data?.data
      if (t && currentTask.value && currentTask.value.id === taskId) {
        currentTask.value.status = t.status
        currentTask.value.progress = t.progress
        currentTask.value.speed = t.speed
        currentTask.value.downloadedSegments = t.downloadedSegments
        currentTask.value.totalSegments = t.totalSegments
        currentTask.value.totalBytes = t.totalBytes
        currentTask.value.downloadedBytes = t.downloadedBytes
        if (t.outputPath) currentTask.value.outputPath = t.outputPath
        if (t.error) currentTask.value.error = t.error
        if (t.status === 'completed' || t.status === 'failed') {
          isDownloading.value = false
        }
      }
    } catch {
      // 静默：WS 才是主通道，REST 仅兜底，不打日志骚扰
    } finally {
      polling = false
    }
  }

  // startPolling 仅在任务活跃时每 3 秒拉取一次，结束即停
  const startPolling = (taskId: string) => {
    stopPolling()
    pollTimer = setInterval(() => {
      const t = currentTask.value
      if (!t || t.id !== taskId || !ACTIVE.includes(t.status)) {
        stopPolling()
        return
      }
      syncTaskFromServer(taskId)
    }, 3000)
  }

  // closeConnection 主动拆除连接：标记后不再重连，并停止兜底轮询
  const closeConnection = () => {
    wsManualClosed = true
    activeTaskId = null
    clearRetryTimer()
    stopPolling()
    if (ws.value) {
      ws.value.close()
      ws.value = null
    }
  }

  const addLog = (level: LogEntry['level'], message: string) => {
    const now = new Date()
    const pad = (n: number) => n.toString().padStart(2, '0')
    const timestamp = `[${now.getFullYear()}.${pad(now.getMonth() + 1)}.${pad(now.getDate())}-${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}]`
    
    logs.value.push({
      level,
      message,
      timestamp
    })
    if (logs.value.length > 200) {
      logs.value.shift()
    }
  }

  const connectWebSocket = (taskId: string) => {
    // 清理旧连接与重试
    wsManualClosed = false
    activeTaskId = taskId
    clearRetryTimer()
    if (ws.value) {
      ws.value.close()
      ws.value = null
    }

    const openSocket = () => {
      // 任务已结束或被重置则不再重连
      if (wsManualClosed || activeTaskId !== taskId) return

      const wsUrl = buildWsUrl(`/ws?taskId=${taskId}`)
      const socket = new WebSocket(wsUrl)
      ws.value = socket

      socket.onopen = () => {
        wsRetryAttempts = 0
        // 连接成功后立即拉一次，补齐建连前漏掉的进度/状态
        syncTaskFromServer(taskId)
      }

      socket.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data)

          switch (data.type) {
            case 'progress':
              if (currentTask.value && data.taskId === currentTask.value.id) {
                currentTask.value.progress = data.progress
                currentTask.value.speed = data.speed
                currentTask.value.downloadedSegments = data.downloadedSegments
                currentTask.value.totalSegments = data.totalSegments
              }
              break

            case 'log':
              addLog(data.level, data.message)
              break

            case 'status':
              if (currentTask.value && data.taskId === currentTask.value.id) {
                currentTask.value.status = data.status
                if (data.message) {
                  addLog('info', data.message)
                  currentTask.value.error = data.message
                } else {
                  currentTask.value.error = ''
                }
                if (data.status === 'completed' || data.status === 'failed') {
                  isDownloading.value = false
                  if (data.status === 'completed' && data.outputPath) {
                    currentTask.value.outputPath = data.outputPath
                  }
                }
              }
              break
          }
        } catch (error) {
          console.error('WebSocket message parse error:', error)
        }
      }

      socket.onerror = (error) => {
        console.error('WebSocket error:', error)
      }

      socket.onclose = () => {
        // 仅在当前连接仍是该 socket、任务仍活跃且非主动关闭时重连
        if (wsManualClosed || activeTaskId !== taskId || ws.value !== socket) return
        const t = currentTask.value
        const stillActive = !!t && t.id === taskId && ACTIVE.includes(t.status)
        if (!stillActive) return

        wsRetryAttempts += 1
        // 指数退避：1s、2s、4s…上限 10s
        const delay = Math.min(1000 * 2 ** (wsRetryAttempts - 1), 10000)
        clearRetryTimer()
        wsRetryTimer = setTimeout(() => {
          openSocket()
        }, delay)
      }
    }

    openSocket()
    // REST 兜底：无论 WS 是否可用，活跃期间周期性同步状态
    startPolling(taskId)
  }

  const startDownload = async (params: {
    url: string
    threadCount?: number
    outputName?: string
    hostType?: string
    cookie?: string
    referer?: string
    autoClear?: boolean
    savePath?: string
    enableWebDAV?: boolean
    webDAVURL?: string
    webDAVUsername?: string
    webDAVPassword?: string
    webDAVRemoteDir?: string
    deleteAfterUpload?: boolean
  }) => {
    try {
      const response = await downloadAPI.start(params)
      const data = response.data
      if (data.code === 200) {
        currentTask.value = {
          id: data.data.taskId,
          url: params.url,
          name: params.outputName || 'movie',
          status: 'downloading',
          progress: 0,
          speed: '0 KB/s',
          totalSegments: 0,
          downloadedSegments: 0,
          createdAt: new Date().toISOString()
        }
        isDownloading.value = true
        addLog('info', `任务已启动: ${params.url}`)
        connectWebSocket(data.data.taskId)
        return data.data
      } else {
        throw new Error(data.message || '启动下载失败')
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '启动下载失败'
      addLog('error', errorMessage)
      throw error
    }
  }

  const pauseDownload = async () => {
    if (!currentTask.value) return
    try {
      await downloadAPI.pause(currentTask.value.id)
      currentTask.value.status = 'paused'
      isDownloading.value = false
      addLog('info', '下载已暂停')
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '暂停失败'
      addLog('error', errorMessage)
    }
  }

  const resumeDownload = async () => {
    if (!currentTask.value) return
    try {
      await downloadAPI.resume(currentTask.value.id)
      currentTask.value.status = 'downloading'
      isDownloading.value = true
      addLog('info', '下载已恢复')
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '恢复失败'
      addLog('error', errorMessage)
    }
  }

  const retryDownload = async (taskId: string, mode?: string) => {
    try {
      addLog('info', '正在重新启动下载任务...')
      const response = await downloadAPI.retry(taskId, mode)
      if (response.data.code === 200) {
        addLog('info', '重试成功，开始下载')
        if (currentTask.value?.id === taskId) {
          isDownloading.value = true
          connectWebSocket(taskId)
        }
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '重试失败'
      addLog('error', errorMessage)
    }
  }

  const retryUpload = async (taskId: string) => {
    try {
      addLog('info', '正在重新尝试 WebDAV 上传...')
      const response = await downloadAPI.upload(taskId)
      if (response.data.code === 200) {
        addLog('info', '上传任务已重新提交')
        if (currentTask.value?.id === taskId) {
          currentTask.value.status = 'uploading'
          isDownloading.value = true
          connectWebSocket(taskId)
        }
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '重试上传失败'
      addLog('error', errorMessage)
    }
  }

  const uploadTask = async (taskId: string, config?: Record<string, unknown>) => {
    try {
      addLog('info', '正在启动 WebDAV 上传...')
      const response = await downloadAPI.upload(taskId, config)
      if (response.data.code === 200) {
        addLog('info', '上传任务已提交')
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '上传失败'
      addLog('error', errorMessage)
    }
  }

  const stopDownload = async () => {
    if (!currentTask.value) return
    await stopDownloadById(currentTask.value.id)
  }

  const stopDownloadById = async (taskId: string) => {
    try {
      await downloadAPI.stop(taskId)
      
      if (currentTask.value?.id === taskId) {
        closeConnection()
        currentTask.value.status = 'failed'
        isDownloading.value = false
      }
      addLog('info', `任务 ${taskId} 已停止`)
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '停止失败'
      addLog('error', errorMessage)
    }
  }

  const pauseTaskById = async (taskId: string) => {
    try {
      await downloadAPI.pause(taskId)
      if (currentTask.value?.id === taskId) {
        currentTask.value.status = 'paused'
        isDownloading.value = false
      }
      addLog('info', `任务 ${taskId} 已暂停`)
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      addLog('error', err.response?.data?.message || err.message || '暂停失败')
    }
  }

  const resumeTaskById = async (taskId: string) => {
    try {
      await downloadAPI.resume(taskId)
      if (currentTask.value?.id === taskId) {
        currentTask.value.status = 'downloading'
        isDownloading.value = true
      }
      addLog('info', `任务 ${taskId} 已恢复`)
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      addLog('error', err.response?.data?.message || err.message || '恢复失败')
    }
  }

  const analyzeM3U8 = async (url: string, referer?: string, cookie?: string) => {
    try {
      addLog('info', `正在分析链接: ${url}`)
      const response = await downloadAPI.analyze(url, referer, cookie)
      const data = response.data
      if (data.code === 200) {
        const info = data.data
        if (info.type === 'm3u8') {
          addLog('info', `分析完成: 发现 ${info.segments} 个片段${info.hasKey ? ' (加密视频)' : ' (未加密)'}`)
        } else {
          addLog('info', '分析完成: 检测为通用视频文件直链')
        }
        return true
      } else {
        addLog('error', `分析失败: ${data.message}`)
        return false
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '未知错误'
      addLog('error', `分析请求失败: ${errorMessage}`)
      return false
    }
  }

  const reset = () => {
    closeConnection()
    currentTask.value = null
    isDownloading.value = false
  }

  return {
    currentTask,
    logs,
    isDownloading,
    addLog,
    startDownload,
    pauseDownload,
    resumeDownload,
    retryDownload,
    retryUpload,
    stopDownload,
    stopDownloadById,
    pauseTaskById,
    resumeTaskById,
    reset,
    analyzeM3U8,
    uploadTask
  }
})
