<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { taskAPI, settingsAPI } from '@/api'
import { useDownloadStore } from '@/stores/download'
import { useSettingsStore } from '@/stores/settings'
import WebDAVBrowser from './WebDAVBrowser.vue'
import type { Task } from '@/stores/download'

const downloadStore = useDownloadStore()
const settingsStore = useSettingsStore()
const tasks = ref<Task[]>([])
const filter = ref<'all' | 'pending' | 'downloading' | 'merging' | 'uploading' | 'completed' | 'failed' | 'paused'>('all')
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

const openUploadBrowser = async (task: Task) => {
  selectedTaskId.value = task.id
  
  // 尝试获取任务自带的配置，如果没有则从全局设置中加载
  if (task.webDAVURL) {
    webdavConfig.value = {
      url: task.webDAVURL,
      username: task.webDAVUsername || '',
      password: task.webDAVPassword || '',
      remoteDir: task.webDAVRemoteDir || ''
    }
    showBrowser.value = true
  } else {
    // 加载全局设置
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
          alert('请先在设置中配置 WebDAV 地址，或手动填写地址')
        }
        showBrowser.value = true
      }
    } catch (err) {
      alert('加载 WebDAV 配置失败')
    }
  }
}

const onDirSelect = (path: string) => {
  downloadStore.uploadTask(selectedTaskId.value, {
    enabled: true,
    url: webdavConfig.value.url,
    username: webdavConfig.value.username,
    password: webdavConfig.value.password,
    remoteDir: path
  })
}

const wsRetryTimer = ref<number | null>(null)

const connectGlobalWebSocket = () => {
  if (ws.value) {
    ws.value.close()
  }

  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const wsUrl = `${protocol}//${window.location.host}/ws` // taskId is empty for global updates

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
            task.error = data.message || '' // 实时更新消息内容（如“下载并上传完成”）
            if (data.status === 'completed' && data.outputPath) {
              task.outputPath = data.outputPath
            }
            break
        }
      } else if (data.type === 'status' && data.status === 'downloading') {
        // Optional: if a new task started, we might want to refresh the list
        loadTasks()
      }
    } catch (error) {
      console.error('Global WebSocket error:', error)
    }
  }

  ws.value.onclose = () => {
    // Retry connection after a delay if still on this page
    if (wsRetryTimer.value) window.clearTimeout(wsRetryTimer.value)
    wsRetryTimer.value = window.setTimeout(() => {
      if (ws.value === null) return // already unmounted
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
  
  // 严格按创建时间排序，确保“队列顺序”
  return [...result].sort((a, b) => {
    const timeA = new Date(a.createdAt).getTime()
    const timeB = new Date(b.createdAt).getTime()
    
    if (timeA !== timeB) {
      return settingsStore.settings.taskSortOrder === 'desc' 
        ? timeB - timeA 
        : timeA - timeB
    }
    
    // 如果时间完全一致（极少见），按 ID 排序以保证排序稳定性
    return a.id.localeCompare(b.id)
  })
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
      return 'text-yellow-500 bg-yellow-500/10'
    case 'downloading':
      return 'text-primary bg-primary/10'
    case 'merging':
      return 'text-purple-400 bg-purple-400/10'
    case 'uploading':
      return 'text-blue-400 bg-blue-400/10'
    case 'paused':
      return 'text-yellow-400 bg-yellow-400/10'
    case 'completed':
      return 'text-green-400 bg-green-400/10'
    case 'failed':
      return 'text-red-400 bg-red-400/10'
    default:
      return 'text-gray-400 bg-gray-400/10'
  }
}

const formatSize = (kb: number) => {
  if (kb <= 0) return '0 KB'
  if (kb < 1024) return `${kb} KB`
  return `${(kb / 1024).toFixed(1)} MB`
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
          v-for="f in ['all', 'pending', 'downloading', 'merging', 'uploading', 'paused', 'completed', 'failed']"
          :key="f"
          @click="filter = f as any"
          class="px-4 py-2 rounded-lg text-sm font-medium transition-colors whitespace-nowrap"
          :class="filter === f
            ? 'bg-primary text-dark-200'
            : 'bg-dark-300 text-gray-400 hover:text-white'"
        >
          {{ 
            f === 'all' ? '全部' : 
            f === 'pending' ? '等待中' :
            f === 'downloading' ? '下载中' : 
            f === 'merging' ? '合并中' :
            f === 'uploading' ? '上传中' : 
            f === 'paused' ? '已暂停' :
            f === 'completed' ? '已完成' : 
            '失败' 
          }}
        </button>
      </div>

      <div v-if="filteredTasks.length === 0" class="flex flex-col items-center justify-center py-12 text-gray-500">
        <svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round" class="mb-4 opacity-20"><path d="M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5L14.5 2z"/><polyline points="14 2 14 8 20 8"/></svg>
        <p>{{ loading ? '加载中...' : '暂无任务记录' }}</p>
      </div>

      <div v-else class="space-y-4">
        <div
          v-for="task in filteredTasks"
          :key="task.id"
          class="card hover:border-primary/20 transition-all group"
        >
          <div class="flex items-start justify-between mb-3">
            <div class="flex-1 min-w-0">
              <h3 class="font-semibold text-white truncate">
                {{ task.name }}.mp4
              </h3>
              <p class="text-sm text-gray-400 truncate mt-1" :title="task.url">
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
                  task.status === 'uploading' ? '正在上传' :
                  task.status === 'paused' ? '已暂停' :
                  task.status === 'completed' ? '已完成' :
                  task.status === 'failed' ? '失败' : 
                  task.status
                }}
              </span>
              <button
                v-if="task.status === 'failed' || task.status === 'completed'"
                @click="openUploadBrowser(task)"
                class="text-gray-400 hover:text-blue-400 transition-colors"
                title="上传至 WebDAV"
              >
                <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12" />
                </svg>
              </button>
              <button
                v-if="task.status === 'failed' || task.status === 'completed'"
                @click="downloadStore.retryDownload(task.id)"
                class="text-gray-400 hover:text-primary transition-colors"
                title="重试"
              >
                <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                </svg>
              </button>
              <button
                v-if="task.status === 'downloading' || task.status === 'uploading' || task.status === 'paused'"
                @click="downloadStore.stopDownloadById(task.id)"
                class="p-2 text-gray-400 hover:text-red-400 transition-colors"
                title="停止任务"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2" ry="2"/></svg>
              </button>

              <button
                v-if="(task.status === 'failed' || task.status === 'completed') && task.outputPath"
                @click="downloadStore.retryUpload(task.id)"
                class="p-2 text-gray-400 hover:text-blue-400 transition-colors"
                title="重试上传"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 2v6h-6"/><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M3 22v-6h6"/><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/></svg>
              </button>

              <button
                @click="deleteTask(task.id)"
                class="p-2 text-gray-400 hover:text-red-400 transition-colors"
                title="删除记录"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>
              </button>
            </div>
          </div>

          <div v-if="task.status === 'downloading' || task.status === 'uploading' || task.status === 'paused'" class="mb-3">
            <div class="flex justify-between text-xs text-gray-400 mb-1">
                <span v-if="task.status === 'uploading'">
                  {{ formatSize(task.downloadedSegments) }} / {{ formatSize(task.totalSegments) }}
                </span>
                <span v-else>
                  {{ task.downloadedSegments }} / {{ task.totalSegments }} 片段
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
            <span v-if="task.speed" class="font-mono">{{ task.speed }}</span>
          </div>

          <div v-if="task.outputPath" class="mt-2 text-xs text-gray-400 font-mono truncate">
            {{ task.outputPath }}
          </div>

          <div v-if="task.error" class="mt-2 text-xs text-red-400">
            {{ task.error }}
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
  </div>
</template>

<style scoped>
</style>
