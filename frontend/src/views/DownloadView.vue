<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useDownloadStore } from '@/stores/download'
import { useSettingsStore } from '@/stores/settings'
import { diskAPI } from '@/api'
import WebDAVBrowser from './WebDAVBrowser.vue'

const downloadStore = useDownloadStore()
const settingsStore = useSettingsStore()

const preDownloadChecking = ref(false)
const spaceError = ref('')

const url = ref('')
const outputName = ref('movie')
const hostType = ref('v1')
const cookie = ref('')
const referer = ref('')
const autoClear = ref(true)

const normalizeReferrer = (value: string): string => {
  value = value.trim()
  if (!value) return ''
  
  if (!value.startsWith('http://') && !value.startsWith('https://')) {
    value = 'https://' + value
  }
  
  if (!value.endsWith('/')) {
    value = value + '/'
  }
  
  return value
}

const handleReferrerBlur = () => {
  referer.value = normalizeReferrer(referer.value)
}
const savePath = ref('')

const enableWebDAV = ref(false)
const webDAVRemoteDir = ref('')
const deleteAfterUpload = ref(false)

const showBrowser = ref(false)
const showMoreParams = ref(false)
const analyzing = ref(false)

const showRetryModal = ref(false)

const handleRetryClick = () => {
  if (downloadStore.currentTask?.url?.toLowerCase().includes('.m3u8')) {
    showRetryModal.value = true
  } else {
    downloadStore.retryDownload(downloadStore.currentTask!.id)
  }
}

const confirmRetry = (mode: string) => {
  showRetryModal.value = false
  
  if (mode === 'retry_upload') {
    if (downloadStore.currentTask) {
      downloadStore.retryUpload(downloadStore.currentTask.id)
    }
  } else {
    downloadStore.retryDownload(downloadStore.currentTask!.id, mode)
  }
}

const onDirSelect = (path: string) => {
  webDAVRemoteDir.value = path
}

let lastAnalyzedUrl = ''
const handleUrlChange = async () => {
  const currentUrl = url.value.trim()
  if (!currentUrl) return
  
  const m3u8Regex = /\.m3u8/i
  if (m3u8Regex.test(currentUrl) && currentUrl !== lastAnalyzedUrl) {
    lastAnalyzedUrl = currentUrl
    analyzing.value = true
    try {
      await downloadStore.analyzeM3U8(currentUrl, referer.value, cookie.value)
    } finally {
      analyzing.value = false
    }
  }
}

const manualAnalyze = async () => {
  const currentUrl = url.value.trim()
  if (!currentUrl) return
  
  analyzing.value = true
  try {
    await downloadStore.analyzeM3U8(currentUrl, referer.value, cookie.value)
  } finally {
    analyzing.value = false
  }
}

onMounted(async () => {
  await settingsStore.loadSettings()
  outputName.value = settingsStore.settings.defaultOutputName
  hostType.value = settingsStore.settings.hostType
  autoClear.value = settingsStore.settings.autoClear
  savePath.value = settingsStore.settings.defaultSavePath
  referer.value = settingsStore.settings.defaultReferer
  
  enableWebDAV.value = settingsStore.settings.enableWebDAV
  webDAVRemoteDir.value = settingsStore.settings.webDAVRemoteDir
  deleteAfterUpload.value = settingsStore.settings.deleteAfterUpload
})

// 监听下载状态，确保下载开始时重置分析状态
watch(() => downloadStore.isDownloading, (isDownloading) => {
  if (isDownloading) {
    analyzing.value = false
  }
})

const canStart = computed(() => {
  return url.value.trim() !== '' && !downloadStore.isDownloading
})

const canPause = computed(() => {
  return downloadStore.isDownloading && downloadStore.currentTask?.status === 'downloading'
})

const canResume = computed(() => {
  return downloadStore.currentTask?.status === 'paused'
})

const queuedTasks = ref<{ url: string; params: Record<string, unknown>; timestamp: number }[]>([])
const isWaiting = ref(false)

const checkDiskSpaceAndStart = async (taskParams?: any) => {
  const params = taskParams || {
    threadCount: settingsStore.settings.defaultThreadCount,
    outputName: outputName.value.trim(),
    hostType: hostType.value,
    cookie: cookie.value.trim(),
    referer: referer.value.trim(),
    autoClear: autoClear.value,
    savePath: savePath.value,
    enableWebDAV: enableWebDAV.value,
    webDAVURL: settingsStore.settings.webDAVURL,
    webDAVUsername: settingsStore.settings.webDAVUsername,
    webDAVPassword: settingsStore.settings.webDAVPassword,
    webDAVRemoteDir: webDAVRemoteDir.value,
    deleteAfterUpload: deleteAfterUpload.value
  }

  if (settingsStore.settings.enablePreDownloadCheck) {
    preDownloadChecking.value = true
    try {
      const response = await diskAPI.checkSpace(savePath.value)
      if (response.data.code === 200 && response.data.data) {
        const freeMB = response.data.data.free / (1024 * 1024)
        const requiredMB = settingsStore.settings.minFreeSpaceMB
        
        let requiredEstimatedMB = 0
        if (downloadStore.currentTask && downloadStore.currentTask.totalSegments > 0) {
          requiredEstimatedMB = downloadStore.currentTask.totalSegments * 0.5
        }
        
        if (freeMB - requiredEstimatedMB < requiredMB) {
          spaceError.value = `磁盘空间不足！预计需要 ${requiredEstimatedMB.toFixed(0)} MB，当前可用 ${freeMB.toFixed(0)} MB，建议至少保留 ${requiredMB} MB。任务已加入等待队列，将在空间充足时自动开始。`
          preDownloadChecking.value = false
          
          queuedTasks.value.push({
            url: url.value.trim(),
            params: params,
            timestamp: Date.now()
          })
          
          isWaiting.value = true
          startWaitCheckLoop()
          return { canProceed: false, fromQueue: false }
        }
      }
    } catch (error) {
      console.error('Failed to check disk space:', error)
    }
    preDownloadChecking.value = false
  }
  return { canProceed: true, fromQueue: false, params: params }
}

let waitCheckTimer: ReturnType<typeof setInterval> | null = null

const startWaitCheckLoop = () => {
  if (waitCheckTimer) {
    clearInterval(waitCheckTimer)
  }
  waitCheckTimer = setInterval(async () => {
    if (queuedTasks.value.length === 0) {
      isWaiting.value = false
      stopWaitCheckLoop()
      return
    }

    if (downloadStore.isDownloading) {
      return
    }

    try {
      const response = await diskAPI.checkSpace(savePath.value)
      if (response.data.code === 200 && response.data.data) {
        const freeMB = response.data.data.free / (1024 * 1024)
        const requiredMB = settingsStore.settings.minFreeSpaceMB
        
        if (freeMB >= requiredMB + 100) {
          const task = queuedTasks.value.shift()
          if (task) {
            spaceError.value = ''
            isWaiting.value = false
            stopWaitCheckLoop()
            const result = await checkDiskSpaceAndStart(task.params)
            if (result.canProceed) {
              await downloadStore.startDownload(task.params as any)
            }
          }
        }
      }
    } catch (error) {
      console.error('Failed to check disk space in wait loop:', error)
    }
  }, 5000)
}

const stopWaitCheckLoop = () => {
  if (waitCheckTimer) {
    clearInterval(waitCheckTimer)
    waitCheckTimer = null
  }
}

// 立即开始等待队列中的任务
const startQueuedTaskImmediately = async () => {
  if (queuedTasks.value.length === 0) return
  
  const task = queuedTasks.value.shift()
  if (task) {
    spaceError.value = ''
    isWaiting.value = false
    stopWaitCheckLoop()
    const result = await checkDiskSpaceAndStart(task.params)
    if (result.canProceed) {
      await downloadStore.startDownload(task.params as any)
    }
  }
}

// 清空等待队列
const clearQueue = () => {
  queuedTasks.value = []
  spaceError.value = ''
  isWaiting.value = false
  stopWaitCheckLoop()
}

const startDownload = async () => {
  if (!canStart.value) return

  // 重置分析状态，确保下载开始后不再显示"分析中..."
  analyzing.value = false
  
  spaceError.value = ''

  const result = await checkDiskSpaceAndStart()
  if (!result.canProceed) {
    return
  }

  try {
    await downloadStore.startDownload({
      url: url.value.trim(),
      ...result.params
    })
  } catch (error) {
    console.error('Download failed:', error)
  }
}

const pauseDownload = () => {
  downloadStore.pauseDownload()
}

const resumeDownload = () => {
  downloadStore.resumeDownload()
}

const stopDownload = () => {
  downloadStore.stopDownload()
}

const reset = () => {
  downloadStore.reset()
  url.value = ''
  outputName.value = settingsStore.settings.defaultOutputName
  hostType.value = settingsStore.settings.hostType
  autoClear.value = settingsStore.settings.autoClear
  savePath.value = settingsStore.settings.defaultSavePath
  enableWebDAV.value = settingsStore.settings.enableWebDAV
  webDAVRemoteDir.value = settingsStore.settings.webDAVRemoteDir
  deleteAfterUpload.value = settingsStore.settings.deleteAfterUpload
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
const estimatedTime = computed(() => {
  const task = downloadStore.currentTask
  if (!task) return ''
  
  const status = task.status
  if (status === 'completed' || status === 'failed' || status === 'pending' || status === 'paused') {
    return ''
  }
  
  const speedKBps = parseSpeed(task.speed)
  if (speedKBps <= 0) return ''
  
  let downloadedKB: number
  let totalKB: number
  
  if ((status === 'downloading' || status === 'merging') && isM3U8Download.value) {
    // M3U8下载按片段计算，假设每片段约0.5MB
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
})

const isM3U8Download = computed(() => {
  return downloadStore.currentTask?.url?.toLowerCase().includes('.m3u8') ?? false
})

const isTaskCompleted = computed(() => {
  const task = downloadStore.currentTask
  if (!task) return false
  return task.status === 'completed' || task.status === 'failed'
})

watch(isTaskCompleted, async (completed) => {
  if (completed && queuedTasks.value.length > 0) {
    // 延迟一秒后检查队列，确保状态完全更新
    setTimeout(() => {
      startWaitCheckLoop()
    }, 1000)
  }
})
</script>

<template>
  <div class="space-y-6">
    <div class="card">
      <h2 class="text-2xl font-bold mb-6 text-primary">下载 M3U8 视频</h2>

      <div class="space-y-4">
        <div>
          <label class="block text-sm font-medium text-gray-600 mb-2">
            M3U8 地址
          </label>
          <div class="flex gap-2">
            <input
              v-model="url"
              type="text"
              placeholder="输入或粘贴 m3u8 链接..."
              class="input-field"
              :disabled="downloadStore.isDownloading"
              @input="handleUrlChange"
            />
            <button 
              @click="manualAnalyze"
              :disabled="!url.trim() || downloadStore.isDownloading || analyzing"
              class="btn-secondary whitespace-nowrap px-4 flex items-center gap-2"
              title="分析 M3U8 链接"
            >
              <svg v-if="analyzing" class="animate-spin h-4 w-4" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
              {{ analyzing ? '分析中...' : '分析' }}
            </button>
          </div>
        </div>

        <div>
          <label class="block text-sm font-medium text-gray-600 mb-2">
            输出文件名
          </label>
          <input
            v-model="outputName"
            type="text"
            placeholder="movie"
            class="input-field"
            :disabled="downloadStore.isDownloading"
          />
        </div>

        <!-- 更多参数折叠块 -->
        <div class="more-params-container">
          <button 
            @click="showMoreParams = !showMoreParams"
            class="more-params-header"
          >
            <div class="flex items-center gap-2">
              <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" :class="{'rotate-90': showMoreParams}" class="transition-transform"><path d="m9 18 6-6-6-6"/></svg>
              更多参数
            </div>
            <span class="text-xs font-normal">此处参数高于全局设置</span>
          </button>
          
          <div v-show="showMoreParams" class="more-params-content">
            <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <label class="block text-sm font-medium mb-2">
                  Host 类型
                </label>
                <select v-model="hostType" class="input-field" :disabled="downloadStore.isDownloading">
                  <option value="v1">V1 (完整路径)</option>
                  <option value="v2">V2 (仅域名)</option>
                </select>
              </div>

              <div>
                <label class="block text-sm font-medium mb-2">
                  保存路径
                </label>
                <input
                  v-model="savePath"
                  type="text"
                  placeholder="默认当前目录"
                  class="input-field"
                  :disabled="downloadStore.isDownloading"
                />
              </div>
            </div>

            <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mt-4">
              <div>
                <label class="block text-sm font-medium mb-2">
                  自定义主机名 (Referer)
                </label>
                <input
                  v-model="referer"
                  type="text"
                  placeholder="https://example.com/ (用于绕过鉴权)"
                  class="input-field"
                  :disabled="downloadStore.isDownloading"
                  @blur="handleReferrerBlur"
                />
              </div>

              <div>
                <label class="block text-sm font-medium mb-2">
                  Cookie (可选)
                </label>
                <input
                  v-model="cookie"
                  type="text"
                  placeholder="key1=value1;key2=value2"
                  class="input-field"
                  :disabled="downloadStore.isDownloading"
                />
              </div>
            </div>

            <br>

            <div class="flex items-center space-x-2 pt-4 border-t border-gray-300">
              <input
                v-model="autoClear"
                type="checkbox"
                id="autoClear"
                class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded focus:ring-primary"
                :disabled="downloadStore.isDownloading"
              />
              <label for="autoClear" class="text-sm text-gray-600">
                下载完成后自动清理临时文件
              </label>
            </div>

            <div class="border-t border-gray-300 pt-4 mt-4">
              <div class="flex items-center space-x-2 mb-4">
                <input
                  v-model="enableWebDAV"
                  type="checkbox"
                  id="enableWebDAV"
                  class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded focus:ring-primary"
                  :disabled="downloadStore.isDownloading"
                />
                <label for="enableWebDAV" class="text-sm font-medium text-gray-600">
                  启用 WebDAV 中转
                </label>
              </div>

              <div v-if="enableWebDAV" class="space-y-4 pl-6 border-l-2 border-gray-300">
                <div>
                  <label class="block text-sm font-medium text-gray-600 mb-2">
                    远程目录 (可选)
                  </label>
                  <div class="flex gap-2">
                    <input
                      v-model="webDAVRemoteDir"
                      type="text"
                      placeholder="/videos/movies"
                      class="input-field"
                      :disabled="downloadStore.isDownloading"
                    />
                    <button 
                      @click="showBrowser = true"
                      :disabled="downloadStore.isDownloading || !settingsStore.settings.webDAVURL"
                      class="btn-secondary whitespace-nowrap px-4"
                      title="浏览远程目录"
                    >
                      <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg>
                    </button>
                  </div>
                  <p class="text-xs text-gray-500 mt-1">
                    此处目录优先级高于全局设置
                  </p>
                </div>

                <div class="flex items-center space-x-2">
                  <input
                    v-model="deleteAfterUpload"
                    type="checkbox"
                    id="deleteAfterUpload"
                    class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded focus:ring-primary"
                    :disabled="downloadStore.isDownloading"
                  />
                  <label for="deleteAfterUpload" class="text-sm text-gray-600">
                    上传完成后删除本地文件
                  </label>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div v-if="spaceError" class="bg-red-50 border border-red-200 rounded-lg p-3 mb-4">
          <p class="text-sm text-red-600">{{ spaceError }}</p>
          <div class="flex gap-2 mt-3">
            <button 
              @click="startQueuedTaskImmediately"
              :disabled="downloadStore.isDownloading"
              class="btn-primary text-sm flex items-center gap-2"
            >
              <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 3v4"/><path d="M19 3v4"/><path d="M21 12h-4"/><path d="M5 12H1"/><path d="M21 21h-4"/><path d="M5 21H1"/><path d="M15 15l6-6"/><path d="M9 5l6 6"/></svg>
              立即开始
            </button>
            <button 
              @click="clearQueue"
              class="btn-secondary text-sm"
            >
              清空队列
            </button>
          </div>
        </div>

        <div class="flex flex-wrap gap-3 pt-4">
          <button
            v-if="!downloadStore.isDownloading"
            @click="startDownload"
            :disabled="!canStart || preDownloadChecking"
            class="btn-primary flex items-center gap-2"
          >
            <svg v-if="preDownloadChecking" class="animate-spin h-4 w-4" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
              <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
            </svg>
            {{ preDownloadChecking ? '检查空间中...' : '开始下载' }}
          </button>

          <button
            v-if="downloadStore.isDownloading && canPause"
            @click="pauseDownload"
            class="btn-secondary"
          >
            暂停
          </button>

          <button
            v-if="canResume"
            @click="resumeDownload"
            class="btn-primary"
          >
            继续
          </button>

          <button
            v-if="downloadStore.isDownloading || canResume"
            @click="stopDownload"
            class="btn-secondary text-red-400 hover:text-red-300 hover:border-red-400"
          >
            停止
          </button>

          <button
            v-if="!downloadStore.isDownloading && downloadStore.currentTask && (downloadStore.currentTask.status === 'failed' || downloadStore.currentTask.status === 'completed')"
            @click="handleRetryClick"
            class="btn-primary"
          >
            重试下载
          </button>

          <button
            v-if="!downloadStore.isDownloading && downloadStore.currentTask && (downloadStore.currentTask.status === 'failed' || downloadStore.currentTask.status === 'completed') && downloadStore.currentTask.outputPath"
            @click="downloadStore.retryUpload(downloadStore.currentTask.id)"
            class="btn-secondary text-blue-400 border-blue-400/30 hover:bg-blue-400/10"
          >
            重试上传
          </button>

          <button
            v-if="downloadStore.currentTask"
            @click="reset"
            class="btn-secondary"
            title="将当前任务转入后台并创建新任务"
          >
            新建任务
          </button>
        </div>
      </div>
    </div>

    <div v-if="downloadStore.currentTask" class="card">
      <h3 class="text-lg font-semibold mb-4 text-gray-800">下载进度</h3>

      <div class="space-y-4">
        <div>
          <div class="flex justify-between text-sm text-gray-500 mb-2">
            <span v-if="(downloadStore.currentTask.status === 'downloading' || downloadStore.currentTask.status === 'merging') && isM3U8Download">
              {{ downloadStore.currentTask.downloadedSegments }} / {{ downloadStore.currentTask.totalSegments }} 片段
            </span>
            <span v-else>
              {{ formatSize(downloadStore.currentTask.downloadedSegments) }} / {{ formatSize(downloadStore.currentTask.totalSegments) }}
            </span>
            <span>{{ downloadStore.currentTask.progress.toFixed(1) }}%</span>
          </div>
          <div class="progress-bar">
            <div
              class="progress-bar-fill"
              :style="{ width: `${downloadStore.currentTask.progress}%` }"
            ></div>
          </div>
        </div>

        <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
          <div>
            <span class="text-gray-500">状态</span>
            <div class="font-medium" :class="{
              'text-yellow-600': downloadStore.currentTask.status === 'pending' || downloadStore.currentTask.status === 'paused',
              'text-primary': downloadStore.currentTask.status === 'downloading',
              'text-purple-600': downloadStore.currentTask.status === 'merging',
              'text-blue-600': downloadStore.currentTask.status === 'uploading',
              'text-green-600': downloadStore.currentTask.status === 'completed',
              'text-red-600': downloadStore.currentTask.status === 'failed'
            }">
              {{ 
                downloadStore.currentTask.status === 'pending' ? '等待队列' :
                downloadStore.currentTask.status === 'downloading' ? '正在下载' :
                downloadStore.currentTask.status === 'merging' ? '正在合并' :
                downloadStore.currentTask.status === 'uploading' ? '正在上传' :
                downloadStore.currentTask.status === 'paused' ? '已暂停' :
                downloadStore.currentTask.status === 'completed' ? '已完成' :
                downloadStore.currentTask.status === 'failed' ? '失败' : 
                downloadStore.currentTask.status
              }}
            </div>
          </div>

          <div>
            <span class="text-gray-500">速度</span>
            <div class="font-mono text-gray-800">{{ downloadStore.currentTask.speed }}</div>
          </div>

          <div>
            <span class="text-gray-500">{{ (downloadStore.currentTask.status === 'downloading' || downloadStore.currentTask.status === 'merging') && isM3U8Download ? '片段' : '容量' }}</span>
            <div class="font-mono text-gray-800">
              {{ (downloadStore.currentTask.status === 'downloading' || downloadStore.currentTask.status === 'merging') && isM3U8Download
                ? `${downloadStore.currentTask.downloadedSegments} / ${downloadStore.currentTask.totalSegments}` 
                : `${formatSize(downloadStore.currentTask.downloadedSegments)} / ${formatSize(downloadStore.currentTask.totalSegments)}` 
              }}
            </div>
          </div>

          <div>
            <span class="text-gray-500">URL</span>
            <div class="truncate text-xs text-gray-600" :title="downloadStore.currentTask.url">
              {{ downloadStore.currentTask.url }}
            </div>
          </div>
        </div>

        <div v-if="downloadStore.currentTask.outputPath" class="mt-4 p-3 bg-green-100 rounded-lg border border-green-300">
          <div class="text-sm text-green-600 font-medium mb-1">下载完成!</div>
          <div class="text-xs text-gray-600 font-mono break-all">
            {{ downloadStore.currentTask.outputPath }}
          </div>
        </div>

        <div v-if="downloadStore.currentTask.status === 'failed' && downloadStore.currentTask.error" class="mt-4 p-3 bg-red-100 rounded-lg border border-red-300">
          <div class="text-sm text-red-600 font-medium mb-1">下载失败</div>
          <div class="text-xs text-gray-600">
            {{ downloadStore.currentTask.error }}
          </div>
        </div>

        <!-- 针对 merging 或 uploading 状态显示实时详细信息 -->
        <div v-if="(downloadStore.currentTask.status === 'merging' || downloadStore.currentTask.status === 'uploading') && downloadStore.currentTask.error" class="mt-4 p-3 bg-primary/10 rounded-lg border border-primary/30">
          <div class="flex justify-between items-center">
            <div class="text-sm text-primary font-medium">
              {{ downloadStore.currentTask.status === 'merging' ? '正在合并' : '正在上传' }}
            </div>
            <div v-if="estimatedTime" class="text-xs text-gray-500">
              预计 {{ estimatedTime }} 后完成
            </div>
          </div>
          <div class="text-xs text-gray-600 mt-1">
            {{ downloadStore.currentTask.error }}
          </div>
        </div>
        
        <!-- 下载状态显示预计完成时间 -->
        <div v-if="downloadStore.currentTask.status === 'downloading' && estimatedTime" class="mt-4 p-3 bg-green-50 rounded-lg border border-green-200">
          <div class="flex justify-between items-center">
            <div class="text-sm text-green-600 font-medium">正在下载...</div>
            <div class="text-xs text-gray-500">
              预计 {{ estimatedTime }} 后完成
            </div>
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      <h3 class="text-lg font-semibold mb-4 text-gray-800">下载日志</h3>
      <div class="bg-gray-100 rounded-lg p-4 h-64 overflow-y-auto font-mono text-sm">
        <div v-if="downloadStore.logs.length === 0" class="text-gray-500">
          暂无日志
        </div>
        <div
          v-for="(log, index) in downloadStore.logs"
          :key="index"
          class="mb-1"
          :class="{
            'text-primary': log.level === 'info',
            'text-gray-500': log.level === 'debug',
            'text-yellow-600': log.level === 'warn',
            'text-red-600': log.level === 'error'
          }"
        >
          <span class="text-gray-400">{{ log.timestamp }}</span>
          <span class="ml-2">{{ log.message }}</span>
        </div>
      </div>
    </div>

    <!-- WebDAV 目录选择模态框 -->
    <WebDAVBrowser
      :show="showBrowser"
      :url="settingsStore.settings.webDAVURL"
      :username="settingsStore.settings.webDAVUsername"
      :password="settingsStore.settings.webDAVPassword"
      :initial-path="webDAVRemoteDir"
      @close="showBrowser = false"
      @select="onDirSelect"
    />

    <!-- 重试方式选择模态框 -->
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
            :disabled="!isTaskCompleted"
            class="retry-modal-btn upload-btn"
            :class="{ disabled: !isTaskCompleted }"
          >
            <div class="font-medium">上传至 WebDAV</div>
            <div class="text-sm mt-1">
              {{ isTaskCompleted ? '将已下载完成的文件上传到 WebDAV 服务器' : '需等待下载/合并完成后才能上传' }}
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
</template>

<style scoped>
</style>
