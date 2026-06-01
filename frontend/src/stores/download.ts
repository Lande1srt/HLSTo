import { defineStore } from 'pinia'
import { ref } from 'vue'
import { downloadAPI } from '@/api'

export interface Task {
  id: string
  url: string
  name: string
  status: 'pending' | 'downloading' | 'paused' | 'uploading' | 'completed' | 'failed'
  progress: number
  speed: string
  totalSegments: number
  downloadedSegments: number
  outputPath?: string
  error?: string
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
    if (ws.value) {
      ws.value.close()
    }

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/ws?taskId=${taskId}`

    ws.value = new WebSocket(wsUrl)

    ws.value.onmessage = (event) => {
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

    ws.value.onerror = (error) => {
      console.error('WebSocket error:', error)
      addLog('error', 'WebSocket 连接错误')
    }

    ws.value.onclose = () => {
      console.log('WebSocket closed')
    }
  }

  const startDownload = async (params: {
    url: string
    threadCount?: number
    outputName?: string
    hostType?: string
    cookie?: string
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

  const retryDownload = async (taskId: string) => {
    try {
      addLog('info', '正在重新启动下载任务...')
      const response = await downloadAPI.retry(taskId)
      if (response.data.code === 200) {
        addLog('info', '重试成功，开始下载')
        // 如果是当前正在查看的任务，需要重新连接 WS
        if (currentTask.value?.id === taskId) {
          connectWebSocket(taskId)
        }
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '重试失败'
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
    try {
      await downloadAPI.stop(currentTask.value.id)
      if (ws.value) {
        ws.value.close()
        ws.value = null
      }
      currentTask.value.status = 'failed'
      isDownloading.value = false
      addLog('info', '下载已停止')
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '停止失败'
      addLog('error', errorMessage)
    }
  }

  const analyzeM3U8 = async (url: string) => {
    try {
      addLog('info', `检测到 M3U8 链接，正在分析: ${url}`)
      const response = await downloadAPI.analyze(url)
      const data = response.data
      if (data.code === 200) {
        const info = data.data
        addLog('info', `分析完成: 发现 ${info.segments} 个片段${info.hasKey ? ' (加密视频)' : ' (未加密)'}`)
      } else {
        addLog('warn', `分析失败: ${data.message}`)
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { message?: string } }; message?: string }
      const errorMessage = err.response?.data?.message || err.message || '未知错误'
      addLog('error', `分析请求失败: ${errorMessage}`)
    }
  }

  const reset = () => {
    if (ws.value) {
      ws.value.close()
      ws.value = null
    }
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
    stopDownload,
    reset,
    analyzeM3U8,
    uploadTask
  }
})
