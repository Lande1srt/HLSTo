<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted, computed } from 'vue'
import { taskAPI, settingsAPI, saveBlob } from '@/api'
import { useDownloadStore } from '@/stores/download'
import { useSettingsStore } from '@/stores/settings'
import WebDAVBrowser from './WebDAVBrowser.vue'
import type { Task } from '@/stores/download'

const downloadStore = useDownloadStore()
const settingsStore = useSettingsStore()
const tasks = ref<Task[]>([])
const filter = ref<'all' | 'pending' | 'downloading' | 'merging' | 'compressing' | 'packing' | 'uploading' | 'completed' | 'failed' | 'paused'>('all')
const searchKeyword = ref('')
const loading = ref(false)
const ws = ref<WebSocket | null>(null)

const showBrowser = ref(false)
const selectedTaskId = ref('')
const webdavConfig = ref({
  url: '',
  username: '',
  password: '',
  remoteDir: ''
})

const showRetryModal = ref(false)
const selectedRetryTask = ref<Task | null>(null)

const openRetryModal = (task: Task) => {
  selectedRetryTask.value = task
  showRetryModal.value = true
}

const confirmRetry = (mode: string) => {
  showRetryModal.value = false
  if (!selectedRetryTask.value) return
  
  if (mode === 'retry_upload') {
    selectedTaskId.value = selectedRetryTask.value.id
    
    if (selectedRetryTask.value.webDAVURL) {
      webdavConfig.value = {
        url: selectedRetryTask.value.webDAVURL,
        username: selectedRetryTask.value.webDAVUsername || '',
        password: selectedRetryTask.value.webDAVPassword || '',
        remoteDir: selectedRetryTask.value.webDAVRemoteDir || ''
      }
      showBrowser.value = true
    } else {
      loadWebDAVConfigAndOpenBrowser()
    }
  } else {
    downloadStore.retryDownload(selectedRetryTask.value.id, mode)
  }
}

const loadWebDAVConfigAndOpenBrowser = async () => {
  try {
    const res = await settingsAPI.get()
    if (res.data.code === 200) {
      const s = res.data.data
      webdavConfig.value = {
        url: s.webDAVURL || '',
        username: s.webDAVUsername || '',
        password: s.webDAVPassword || '',
        remoteDir: s.webDAVRemoteDir || ''
      }
      
      if (!webdavConfig.value.url) {
        alert('请先在设置中配置 WebDAV 地址')
        return
      }
      showBrowser.value = true
    }
  } catch (err) {
    alert('加载 WebDAV 配置失败')
  }
}

const isTaskCompleted = (task: Task) => {
  return task.status === 'completed' || task.status === 'failed'
}

const onDirSelect = (path: string) => {
  if (selectedRetryTask.value) {
    downloadStore.retryUpload(selectedRetryTask.value.id)
    selectedRetryTask.value = null
  } else {
    downloadStore.uploadTask(selectedTaskId.value, {
      enabled: true,
      url: webdavConfig.value.url,
      username: webdavConfig.value.username,
      password: webdavConfig.value.password,
      remoteDir: path
    })
  }
}

const wsRetryTimer = ref<number | null>(null)

const connectGlobalWebSocket = () => {
  if (ws.value) {
    ws.value.close()
  }

  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const wsUrl = `${protocol}//${window.location.host}/ws`

  ws.value = new WebSocket(wsUrl)

  ws.value.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data)
      const taskIndex = tasks.value.findIndex(t => t.id === data.taskId)
      
      if (taskIndex !== -1) {
        const task = tasks.value[taskIndex]
        switch (data.type) {
          case 'progress':
            task.progress = data.progress
            task.speed = data.speed
            task.downloadedSegments = data.downloadedSegments
            task.totalSegments = data.totalSegments
            break
          case 'status':
            task.status = data.status
            task.error = data.message || ''
            if (data.status === 'completed' && data.outputPath) {
              task.outputPath = data.outputPath
            }
            break
          case 'log':
            // 日志弹窗打开时实时追加（按 时间戳+内容 去重，避免与定时全量刷新重复）
            if (showLogModal.value && logTask.value?.id === data.taskId) {
              const duplicated = logEntries.value.some(
                e => e.timestamp === data.timestamp && e.message === data.message
              )
              if (!duplicated) {
                logEntries.value.push({
                  level: data.level,
                  message: data.message,
                  timestamp: data.timestamp
                })
              }
            }
            break
        }
      } else if (data.type === 'status' && data.status === 'downloading') {
        loadTasks()
      }
    } catch (error) {
      console.error('Global WebSocket error:', error)
    }
  }

  ws.value.onclose = () => {
    if (wsRetryTimer.value) window.clearTimeout(wsRetryTimer.value)
    wsRetryTimer.value = window.setTimeout(() => {
      if (ws.value === null) return
      connectGlobalWebSocket()
    }, 5000)
  }
}

onUnmounted(() => {
  if (ws.value) {
    ws.value.close()
    ws.value = null
  }
  if (wsRetryTimer.value) {
    window.clearTimeout(wsRetryTimer.value)
    wsRetryTimer.value = null
  }
  stopLogAutoRefresh()
})

const loadTasks = async () => {
  loading.value = true
  try {
    const response = await taskAPI.list()
    const data = response.data
    if (data.code === 200) {
      tasks.value = data.data || []
    }
  } catch (error) {
    console.error('Failed to load tasks:', error)
  } finally {
    loading.value = false
  }
}

const downloadingKey = ref<string>('')

// downloadTaskKey 下载任务关联的 HLS 加密密钥
const downloadTaskKey = async (task: Task) => {
  if (downloadingKey.value) return
  downloadingKey.value = task.id
  try {
    const response = await taskAPI.downloadKey(task.id)
    saveBlob(response.data as Blob, `${task.name || task.id}_enc.key`)
  } catch (error) {
    console.error('Failed to download key:', error)
    alert('密钥下载失败，密钥可能已随任务外的清理操作丢失')
  } finally {
    downloadingKey.value = ''
  }
}

// ============ 任务日志弹窗 ============
interface TaskLogEntry {
  level: string
  message: string
  timestamp: string
}

const showLogModal = ref(false)
const logTask = ref<Task | null>(null)
const logEntries = ref<TaskLogEntry[]>([])
const logLoading = ref(false)
const logFilter = ref('') // '' / debug / info / warn / error
const logOrder = ref<'asc' | 'desc'>('asc')
const logBox = ref<HTMLElement | null>(null)
let logRefreshTimer: number | null = null

// RUNNING_STATUSES 已获取阶段槽位、正在执行的五个阶段状态
const RUNNING_STATUSES: Task['status'][] = ['downloading', 'merging', 'compressing', 'packing', 'uploading']

// isTaskRunning 任务正在五个执行阶段之一运行（可暂停）
const isTaskRunning = (task: Task | null) =>
  !!task && RUNNING_STATUSES.includes(task.status)

const isTaskActive = (task: Task | null) =>
  !!task && ['pending', ...RUNNING_STATUSES, 'paused'].includes(task.status)

// displayedLogs 展示用日志（服务端正序返回，倒序仅在视图层反转）
const displayedLogs = computed(() =>
  logOrder.value === 'asc'
    ? logEntries.value
    : [...logEntries.value].reverse()
)

// 新日志到达：正序跟随到底部，倒序跟随到顶部
watch(displayedLogs, async () => {
  await nextTick()
  if (logBox.value) {
    logBox.value.scrollTop = logOrder.value === 'asc' ? logBox.value.scrollHeight : 0
  }
})

const loadLogEntries = async () => {
  if (!logTask.value) return
  logLoading.value = true
  try {
    const res = await taskAPI.getLogs(logTask.value.id, logFilter.value)
    logEntries.value = res.data?.data || []
  } catch (error) {
    console.error('Failed to load task logs:', error)
  } finally {
    logLoading.value = false
  }
}

const stopLogAutoRefresh = () => {
  if (logRefreshTimer !== null) {
    window.clearInterval(logRefreshTimer)
    logRefreshTimer = null
  }
}

const openLogModal = async (task: Task) => {
  logTask.value = task
  showLogModal.value = true
  await loadLogEntries()

  // 运行中任务 5 秒兜底全量刷新（WebSocket 正常时有实时增量）
  stopLogAutoRefresh()
  logRefreshTimer = window.setInterval(() => {
    if (isTaskActive(logTask.value)) {
      loadLogEntries()
    }
  }, 5000)
}

const closeLogModal = () => {
  showLogModal.value = false
  stopLogAutoRefresh()
  logTask.value = null
  logEntries.value = []
}

const onLogFilterChange = () => loadLogEntries()

const downloadTaskLogs = async () => {
  if (!logTask.value) return
  try {
    const res = await taskAPI.downloadLogs(logTask.value.id)
    saveBlob(res.data as Blob, `${logTask.value.name || logTask.value.id}.log`)
  } catch (error) {
    console.error('Failed to download task logs:', error)
    alert('日志导出失败')
  }
}

// logLevelClass 日志级别徽章配色
const logLevelClass = (level: string) => {
  switch (level) {
    case 'error': return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'warn': return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300'
    case 'info': return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
    default: return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
  }
}

const formatLogTime = (ts: string) => {
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

const deleteTask = async (id: string) => {
  if (!confirm('确定要删除该任务及其记录吗？')) return
  try {
    await taskAPI.delete(id)
    tasks.value = tasks.value.filter((t: { id: string }) => t.id !== id)
    if (downloadStore.currentTask?.id === id) {
      downloadStore.reset()
    }
  } catch (error) {
    console.error('Failed to delete task:', error)
  }
}

const filteredTasks = computed(() => {
  let result = tasks.value
  if (filter.value !== 'all') {
    result = result.filter((t: { status: any }) => t.status === filter.value)
  }
  
  // 搜索过滤
  if (searchKeyword.value.trim()) {
    const keyword = searchKeyword.value.toLowerCase().trim()
    result = result.filter((t: Task) => {
      const name = t.name?.toLowerCase() || ''
      const url = t.url?.toLowerCase() || ''
      const outputPath = t.outputPath?.toLowerCase() || ''
      return name.includes(keyword) || url.includes(keyword) || outputPath.includes(keyword)
    })
  }
  
  return [...result].sort((a, b) => {
    const timeA = new Date(a.createdAt).getTime()
    const timeB = new Date(b.createdAt).getTime()
    
    if (timeA !== timeB) {
      return settingsStore.settings.taskSortOrder === 'desc' 
        ? timeB - timeA 
        : timeA - timeB
    }
    
    return a.id.localeCompare(b.id)
  })
})

// ============ 前端分页：仅渲染当前页，避免任务过多时页面过长 ============
const currentPage = ref(1)
const pageSize = ref(20)
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100]

// 总页数（至少 1）
const totalPages = computed(() =>
  Math.max(1, Math.ceil(filteredTasks.value.length / pageSize.value))
)

// 当前页实际渲染的任务切片
const pagedTasks = computed(() => {
  const start = (currentPage.value - 1) * pageSize.value
  return filteredTasks.value.slice(start, start + pageSize.value)
})

// 翻页（边界夹紧）
const goToPage = (page: number) => {
  currentPage.value = Math.min(Math.max(1, page), totalPages.value)
}

// 筛选条件或排序变化时回到第 1 页，避免停留在越界页码
watch([filter, searchKeyword, () => settingsStore.settings.taskSortOrder], () => {
  currentPage.value = 1
})

// 删除任务/实时刷新导致总页数收缩时，自动夹紧越界的当前页
watch(totalPages, (tp) => {
  if (currentPage.value > tp) {
    currentPage.value = tp
  }
})

const toggleSortOrder = async () => {
  const newOrder = settingsStore.settings.taskSortOrder === 'desc' ? 'asc' : 'desc'
  await settingsStore.saveSettings({
    ...settingsStore.settings,
    taskSortOrder: newOrder
  })
}

const formatDate = (dateStr: string) => {
  const date = new Date(dateStr)
  return date.toLocaleString('zh-CN')
}

const getStatusColor = (status: Task['status']) => {
  switch (status) {
    case 'pending':
      return 'text-yellow-600 bg-yellow-100'
    case 'downloading':
      return 'text-blue-600 bg-blue-100'
    case 'merging':
      return 'text-purple-600 bg-purple-100'
    case 'compressing':
      return 'text-orange-600 bg-orange-100'
    case 'packing':
      return 'text-indigo-600 bg-indigo-100'
    case 'uploading':
      return 'text-blue-600 bg-blue-100'
    case 'paused':
      return 'text-yellow-600 bg-yellow-100'
    case 'completed':
      return 'text-green-600 bg-green-100'
    case 'failed':
      return 'text-red-600 bg-red-100'
    default:
      return 'text-gray-600 bg-gray-100'
  }
}

const formatSize = (kb: number) => {
  if (kb <= 0) return '0 KB'
  if (kb < 1024) return `${kb} KB`
  return `${(kb / 1024).toFixed(1)} MB`
}

// 解析速度字符串为 KB/s
const parseSpeed = (speedStr: string): number => {
  if (!speedStr || speedStr === '0 B/s') return 0
  
  const match = speedStr.match(/([\d.]+)\s*(KB|MB|GB)/)
  if (!match) return 0
  
  const value = parseFloat(match[1])
  const unit = match[2]
  
  switch (unit) {
    case 'KB':
      return value
    case 'MB':
      return value * 1024
    case 'GB':
      return value * 1024 * 1024
    default:
      return 0
  }
}

// 格式化剩余时间
const formatTime = (seconds: number): string => {
  if (seconds <= 0) return '--:--'
  
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const secs = Math.floor(seconds % 60)
  
  if (hours > 0) {
    return `${hours}:${minutes.toString().padStart(2, '0')}:${secs.toString().padStart(2, '0')}`
  }
  return `${minutes}:${secs.toString().padStart(2, '0')}`
}

// 计算预计完成时间
const getEstimatedTime = (task: Task): string => {
  const status = task.status
  if (status === 'completed' || status === 'failed' || status === 'pending' || status === 'paused' || status === 'compressing' || status === 'packing') {
    return ''
  }
  
  const speedKBps = parseSpeed(task.speed)
  if (speedKBps <= 0) return ''
  
  let downloadedKB: number
  let totalKB: number
  
  if ((status === 'downloading' || status === 'merging') && task.url?.toLowerCase().includes('.m3u8')) {
    downloadedKB = task.downloadedSegments * 512
    totalKB = task.totalSegments * 512
  } else {
    downloadedKB = task.downloadedSegments
    totalKB = task.totalSegments
  }
  
  if (totalKB <= 0 || downloadedKB >= totalKB) return ''
  
  const remainingKB = totalKB - downloadedKB
  const remainingSeconds = remainingKB / speedKBps
  
  if (remainingSeconds <= 0) return ''
  
  return formatTime(remainingSeconds)
}

onMounted(() => {
  settingsStore.loadSettings()
  loadTasks()
  connectGlobalWebSocket()
})

onUnmounted(() => {
  if (ws.value) {
    ws.value.close()
    ws.value = null
  }
})
</script>

<template>
  <div class="space-y-6">
    <div class="card">
      <div class="flex items-center justify-between mb-6">
        <h2 class="text-2xl font-bold text-primary">下载任务</h2>
        <div class="flex items-center gap-2">
          <button 
            @click="toggleSortOrder" 
            class="btn-secondary text-sm flex items-center gap-1"
            :title="settingsStore.settings.taskSortOrder === 'desc' ? '当前：新任务在前' : '当前：旧任务在前'"
          >
            <svg v-if="settingsStore.settings.taskSortOrder === 'desc'" xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m3 16 4 4 4-4"/><path d="M7 20V4"/><path d="M11 4h10"/><path d="M11 8h7"/><path d="M11 12h4"/></svg>
            <svg v-else xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m3 8 4-4 4 4"/><path d="M7 4v16"/><path d="M11 12h4"/><path d="M11 16h7"/><path d="M11 20h10"/></svg>
            {{ settingsStore.settings.taskSortOrder === 'desc' ? '最新优先' : '最早优先' }}
          </button>
          <button @click="loadTasks" class="btn-secondary text-sm">
            刷新
          </button>
        </div>
      </div>

      <div class="flex gap-2 mb-4 overflow-x-auto pb-2">
        <button
          v-for="f in ['all', 'pending', 'downloading', 'merging', 'compressing', 'packing', 'uploading', 'paused', 'completed', 'failed']"
          :key="f"
          @click="filter = f as any"
          class="px-4 py-2 rounded-lg text-sm font-medium transition-colors whitespace-nowrap"
          :class="filter === f
            ? 'bg-primary text-white'
            : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
        >
          {{ 
            f === 'all' ? '全部' : 
            f === 'pending' ? '等待中' :
            f === 'downloading' ? '下载中' : 
            f === 'merging' ? '合并中' :
            f === 'compressing' ? '压缩中' :
            f === 'packing' ? '分片中' :
            f === 'uploading' ? '上传中' :
            f === 'paused' ? '已暂停' :
            f === 'completed' ? '已完成' : 
            '失败' 
          }}
        </button>
      </div>

      <!-- 搜索框 -->
      <div class="mb-4">
        <div class="relative">
          <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400">
            <circle cx="11" cy="11" r="8"/>
            <path d="m21 21-4.35-4.35"/>
          </svg>
          <input
            v-model="searchKeyword"
            type="text"
            placeholder="搜索任务名称、URL 或输出路径..."
            class="w-full pl-10 pr-4 py-2 border border-gray-300 rounded-lg focus:ring-primary focus:border-primary outline-none transition-colors bg-white dark:bg-gray-800 text-gray-800 dark:text-gray-200 placeholder-gray-400"
          />
        </div>
      </div>

      <div v-if="filteredTasks.length === 0" class="flex flex-col items-center justify-center py-12 text-gray-500">
        <svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round" class="mb-4 opacity-20"><path d="M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5L14.5 2z"/><polyline points="14 2 14 8 20 8"/></svg>
        <p>{{ loading ? '加载中...' : '暂无任务记录' }}</p>
      </div>

      <div v-else class="space-y-4">
        <div
          v-for="task in pagedTasks"
          :key="task.id"
          class="card hover:shadow-md transition-all"
        >
          <div class="flex items-start justify-between mb-3">
            <div class="flex-1 min-w-0">
              <h3 class="font-semibold text-gray-800 truncate">
                {{ task.name }}.mp4
              </h3>
              <p class="text-sm text-gray-500 truncate mt-1" :title="task.url">
                {{ task.url }}
              </p>
            </div>
            <div class="flex items-center gap-2 ml-4">
              <span
                class="px-3 py-1 rounded-full text-xs font-medium"
                :class="getStatusColor(task.status)"
              >
                {{ 
                  task.status === 'pending' ? '等待队列' :
                  task.status === 'downloading' ? '正在下载' :
                  task.status === 'merging' ? '正在合并' :
                  task.status === 'compressing' ? '正在压缩' :
                  task.status === 'packing' ? '正在分片' :
                  task.status === 'uploading' ? '正在上传' :
                  task.status === 'paused' ? '已暂停' :
                  task.status === 'completed' ? '已完成' :
                  task.status === 'failed' ? '失败' : 
                  task.status
                }}
              </span>
              <button
                @click="openLogModal(task)"
                class="p-2 text-gray-500 hover:text-primary transition-colors"
                title="查看日志"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="8" x2="16" y1="13" y2="13"/><line x1="8" x2="16" y1="17" y2="17"/><line x1="8" x2="10" y1="9" y2="9"/></svg>
              </button>
              <button
                v-if="task.status === 'failed' || task.status === 'completed'"
                @click="openRetryModal(task)"
                class="text-gray-500 hover:text-primary transition-colors"
                title="重试"
              >
                <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                </svg>
              </button>
              <button
                v-if="isTaskRunning(task)"
                @click="downloadStore.pauseTaskById(task.id)"
                class="p-2 text-gray-500 hover:text-amber-500 transition-colors"
                title="暂停任务"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/></svg>
              </button>
              <button
                v-if="task.status === 'paused'"
                @click="downloadStore.resumeTaskById(task.id)"
                class="p-2 text-gray-500 hover:text-green-500 transition-colors"
                title="继续任务"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="6 3 20 12 6 21 6 3"/></svg>
              </button>
              <button
                v-if="isTaskRunning(task) || task.status === 'paused'"
                @click="downloadStore.stopDownloadById(task.id)"
                class="p-2 text-gray-500 hover:text-red-500 transition-colors"
                title="停止任务"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2" ry="2"/></svg>
              </button>

              <button
                v-if="task.keyPath"
                @click="downloadTaskKey(task)"
                :disabled="downloadingKey === task.id"
                class="p-2 text-gray-500 hover:text-primary transition-colors disabled:opacity-50"
                title="下载加密密钥"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/></svg>
              </button>

              <button
                @click="deleteTask(task.id)"
                class="p-2 text-gray-500 hover:text-red-500 transition-colors"
                title="删除记录"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>
              </button>
            </div>
          </div>

          <div v-if="isTaskRunning(task) || task.status === 'paused'" class="mb-3">
            <div class="flex justify-between text-xs text-gray-500 mb-1">
                <span v-if="(task.status === 'downloading' || task.status === 'merging') && task.url?.toLowerCase().includes('.m3u8')">
                  {{ task.downloadedSegments }} / {{ task.totalSegments }} 片段
                </span>
                <span v-else>
                  {{ formatSize(task.downloadedSegments) }} / {{ formatSize(task.totalSegments) }}
                </span>
                <span>{{ task.progress.toFixed(1) }}%</span>
              </div>
            <div class="progress-bar">
              <div
                class="progress-bar-fill"
                :style="{ width: `${task.progress}%` }"
              ></div>
            </div>
          </div>

          <div class="flex items-center justify-between text-xs text-gray-500">
            <span>创建时间: {{ formatDate(task.createdAt) }}</span>
            <span v-if="task.speed" class="font-mono">
              {{ task.speed }}
            </span>
          </div>

          <div v-if="task.outputPath" class="mt-2 text-xs text-gray-600 font-mono truncate">
            {{ task.outputPath }}
          </div>

          <div v-if="task.error" class="mt-2 text-xs flex justify-between items-center">
            <span class="text-red-500">{{ task.error }}</span>
            <span v-if="getEstimatedTime(task)" class="text-green-600">预计 {{ getEstimatedTime(task) }} 完成</span>
          </div>
        </div>

        <!-- 分页控件：总数超过单页时显示 -->
        <div v-if="totalPages > 1" class="flex flex-wrap items-center justify-between gap-3 pt-2">
          <div class="text-xs text-gray-500">
            共 {{ filteredTasks.length }} 条 · 第 {{ currentPage }} / {{ totalPages }} 页
          </div>

          <div class="flex items-center gap-2">
            <label class="text-xs text-gray-500">每页</label>
            <select
              v-model.number="pageSize"
              class="rounded-lg border border-gray-300 bg-white px-2 py-1 text-xs text-gray-700 focus:border-primary focus:outline-none dark:border-gray-600 dark:bg-gray-700 dark:text-gray-200"
              @change="currentPage = 1"
            >
              <option v-for="size in PAGE_SIZE_OPTIONS" :key="size" :value="size">{{ size }}</option>
            </select>

            <button
              class="rounded-lg border border-gray-300 px-3 py-1 text-xs text-gray-600 transition-colors hover:border-primary hover:text-primary disabled:cursor-not-allowed disabled:opacity-40 dark:border-gray-600 dark:text-gray-300"
              :disabled="currentPage <= 1"
              @click="goToPage(1)"
            >首页</button>
            <button
              class="rounded-lg border border-gray-300 px-3 py-1 text-xs text-gray-600 transition-colors hover:border-primary hover:text-primary disabled:cursor-not-allowed disabled:opacity-40 dark:border-gray-600 dark:text-gray-300"
              :disabled="currentPage <= 1"
              @click="goToPage(currentPage - 1)"
            >上一页</button>
            <button
              class="rounded-lg border border-gray-300 px-3 py-1 text-xs text-gray-600 transition-colors hover:border-primary hover:text-primary disabled:cursor-not-allowed disabled:opacity-40 dark:border-gray-600 dark:text-gray-300"
              :disabled="currentPage >= totalPages"
              @click="goToPage(currentPage + 1)"
            >下一页</button>
            <button
              class="rounded-lg border border-gray-300 px-3 py-1 text-xs text-gray-600 transition-colors hover:border-primary hover:text-primary disabled:cursor-not-allowed disabled:opacity-40 dark:border-gray-600 dark:text-gray-300"
              :disabled="currentPage >= totalPages"
              @click="goToPage(totalPages)"
            >末页</button>
          </div>
        </div>
      </div>
    </div>

    <WebDAVBrowser
      :show="showBrowser"
      :url="webdavConfig.url"
      :username="webdavConfig.username"
      :password="webdavConfig.password"
      :initialPath="webdavConfig.remoteDir"
      @close="showBrowser = false"
      @select="onDirSelect"
    />

    <div v-if="showRetryModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50" @click.self="showRetryModal = false">
      <div class="retry-modal-content">
        <h3 class="text-lg font-semibold mb-4">选择重试方式</h3>
        
        <div class="space-y-3">
          <button
            @click="confirmRetry('retry_missing')"
            class="retry-modal-btn"
          >
            <div class="font-medium">重试下载缺失的分片</div>
            <div class="text-sm mt-1">仅重新下载丢失的片段，保留已下载的部分</div>
          </button>

          <button
            @click="confirmRetry('full_redownload')"
            class="retry-modal-btn"
          >
            <div class="font-medium">完全重新下载</div>
            <div class="text-sm mt-1">删除已有文件，从头开始下载所有分片</div>
          </button>

          <button
            @click="confirmRetry('force_merge')"
            class="retry-modal-btn force-merge"
          >
            <div class="font-medium">忽略缺失分片强制合并</div>
            <div class="text-sm mt-1">跳过缺失片段直接生成视频，可能导致播放问题</div>
          </button>

          <button
            @click="confirmRetry('retry_upload')"
            :disabled="!selectedRetryTask || !isTaskCompleted(selectedRetryTask)"
            class="retry-modal-btn upload-btn"
            :class="{ disabled: !selectedRetryTask || !isTaskCompleted(selectedRetryTask) }"
          >
            <div class="font-medium">上传至 WebDAV</div>
            <div class="text-sm mt-1">
              {{ selectedRetryTask && isTaskCompleted(selectedRetryTask) ? '将已下载完成的文件上传到 WebDAV 服务器' : '需等待下载/合并完成后才能上传' }}
            </div>
          </button>
        </div>

        <button
          @click="showRetryModal = false"
          class="retry-modal-cancel"
        >
          取消
        </button>
      </div>
    </div>
  </div>

  <!-- 任务日志弹窗 -->
  <div v-if="showLogModal" class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    @click.self="closeLogModal">
    <div class="flex w-full max-w-4xl max-h-[85vh] flex-col overflow-hidden rounded-xl bg-white shadow-xl dark:bg-gray-800">

      <!-- 头部 -->
      <div class="flex items-center justify-between border-b border-gray-200 px-5 py-4 dark:border-gray-700">
        <div class="min-w-0">
          <h3 class="truncate font-semibold text-gray-800 dark:text-gray-100">
            任务日志：{{ logTask?.name }}
          </h3>
          <p v-if="isTaskActive(logTask)" class="mt-0.5 text-xs text-primary">
            任务进行中，日志实时更新
          </p>
        </div>
        <button @click="closeLogModal"
          class="ml-4 p-1.5 text-gray-400 transition-colors hover:text-gray-600 dark:hover:text-gray-200"
          title="关闭">
          <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
        </button>
      </div>

      <!-- 工具栏 -->
      <div class="flex flex-wrap items-center gap-2 border-b border-gray-200 px-5 py-3 dark:border-gray-700">
        <select v-model="logFilter" @change="onLogFilterChange"
          class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-xs text-gray-700 focus:border-primary focus:outline-none dark:border-gray-600 dark:bg-gray-700 dark:text-gray-200">
          <option value="">全部级别</option>
          <option value="debug">Debug</option>
          <option value="info">Info</option>
          <option value="warn">Warn</option>
          <option value="error">Error</option>
        </select>

        <button @click="logOrder = logOrder === 'asc' ? 'desc' : 'asc'"
          class="rounded-lg border border-gray-300 px-2 py-1.5 text-xs text-gray-600 transition-colors hover:border-primary hover:text-primary dark:border-gray-600 dark:text-gray-300"
          :title="logOrder === 'asc' ? '当前时间正序，点击切换' : '当前时间倒序，点击切换'">
          {{ logOrder === 'asc' ? '时间正序' : '时间倒序' }}
        </button>

        <button @click="loadLogEntries"
          class="rounded-lg border border-gray-300 px-2 py-1.5 text-xs text-gray-600 transition-colors hover:border-primary hover:text-primary dark:border-gray-600 dark:text-gray-300">
          刷新
        </button>

        <button @click="downloadTaskLogs"
          class="ml-auto rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-primary/90">
          导出日志
        </button>
      </div>

      <!-- 日志内容 -->
      <div ref="logBox" class="flex-1 overflow-y-auto bg-gray-50 p-4 dark:bg-gray-900">
        <div v-if="logLoading && logEntries.length === 0" class="py-10 text-center text-xs text-gray-400">
          日志加载中...
        </div>
        <div v-else-if="displayedLogs.length === 0" class="py-10 text-center text-xs text-gray-400">
          暂无日志记录
        </div>
        <div v-else class="space-y-1.5 font-mono text-xs leading-relaxed">
          <div v-for="(entry, idx) in displayedLogs" :key="idx"
            class="flex items-start gap-2 whitespace-pre-wrap break-all">
            <span class="mt-0.5 shrink-0 text-gray-400 dark:text-gray-500">
              {{ formatLogTime(entry.timestamp) }}
            </span>
            <span class="mt-0.5 shrink-0 rounded px-1.5 py-px text-[10px] font-semibold uppercase"
              :class="logLevelClass(entry.level)">
              {{ entry.level }}
            </span>
            <span class="min-w-0 flex-1 text-gray-700 dark:text-gray-200">
              {{ entry.message }}
            </span>
          </div>
        </div>
      </div>

      <!-- 底部统计 -->
      <div class="border-t border-gray-200 px-5 py-2.5 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
        共 {{ logEntries.length }} 条记录
      </div>
    </div>
  </div>
</template>

<style scoped>
</style>
