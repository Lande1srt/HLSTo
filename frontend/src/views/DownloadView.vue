<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useDownloadStore } from '@/stores/download'
import { useSettingsStore } from '@/stores/settings'

const downloadStore = useDownloadStore()
const settingsStore = useSettingsStore()

const url = ref('')
const threadCount = ref(24)
const outputName = ref('movie')
const hostType = ref('v1')
const cookie = ref('')
const autoClear = ref(true)
const savePath = ref('')

const enableWebDAV = ref(false)
const webDAVURL = ref('')
const webDAVUsername = ref('')
const webDAVPassword = ref('')
const webDAVRemoteDir = ref('')
const deleteAfterUpload = ref(false)

let lastAnalyzedUrl = ''
const handleUrlChange = async () => {
  const currentUrl = url.value.trim()
  if (!currentUrl) return
  
  // 匹配包含 .m3u8 的链接（兼容带时间戳或其他参数的情况）
  const m3u8Regex = /\.m3u8/i
  if (m3u8Regex.test(currentUrl) && currentUrl !== lastAnalyzedUrl) {
    lastAnalyzedUrl = currentUrl
    await downloadStore.analyzeM3U8(currentUrl)
  }
}

onMounted(async () => {
  await settingsStore.loadSettings()
  threadCount.value = settingsStore.settings.defaultThreadCount
  outputName.value = settingsStore.settings.defaultOutputName
  hostType.value = settingsStore.settings.hostType
  autoClear.value = settingsStore.settings.autoClear
  savePath.value = settingsStore.settings.defaultSavePath
  
  enableWebDAV.value = settingsStore.settings.enableWebDAV
  webDAVURL.value = settingsStore.settings.webDAVURL
  webDAVUsername.value = settingsStore.settings.webDAVUsername
  webDAVPassword.value = settingsStore.settings.webDAVPassword
  webDAVRemoteDir.value = settingsStore.settings.webDAVRemoteDir
  deleteAfterUpload.value = settingsStore.settings.deleteAfterUpload
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

const startDownload = async () => {
  if (!canStart.value) return

  try {
    await downloadStore.startDownload({
      url: url.value,
      threadCount: threadCount.value,
      outputName: outputName.value,
      hostType: hostType.value,
      cookie: cookie.value,
      autoClear: autoClear.value,
      savePath: savePath.value,
      enableWebDAV: enableWebDAV.value,
      webDAVURL: webDAVURL.value,
      webDAVUsername: webDAVUsername.value,
      webDAVPassword: webDAVPassword.value,
      webDAVRemoteDir: webDAVRemoteDir.value,
      deleteAfterUpload: deleteAfterUpload.value
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
}
</script>

<template>
  <div class="space-y-6">
    <div class="card">
      <h2 class="text-2xl font-bold mb-6 text-primary">下载 M3U8 视频</h2>

      <div class="space-y-4">
        <div>
          <label class="block text-sm font-medium text-gray-300 mb-2">
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
              @click="downloadStore.analyzeM3U8(url)"
              :disabled="!url.trim() || downloadStore.isDownloading"
              class="btn-secondary whitespace-nowrap px-4"
              title="手动分析链接"
            >
              <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>
            </button>
          </div>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label class="block text-sm font-medium text-gray-300 mb-2">
              线程数: {{ threadCount }}
            </label>
            <input
              v-model.number="threadCount"
              type="range"
              min="1"
              max="100"
              class="w-full"
              :disabled="downloadStore.isDownloading"
            />
          </div>

          <div>
            <label class="block text-sm font-medium text-gray-300 mb-2">
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
        </div>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label class="block text-sm font-medium text-gray-300 mb-2">
              Host 类型
            </label>
            <select v-model="hostType" class="input-field" :disabled="downloadStore.isDownloading">
              <option value="v1">V1 (完整路径)</option>
              <option value="v2">V2 (仅域名)</option>
            </select>
          </div>

          <div>
            <label class="block text-sm font-medium text-gray-300 mb-2">
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

        <div>
          <label class="block text-sm font-medium text-gray-300 mb-2">
            Cookie (可选)
          </label>
          <input
            v-model="cookie"
            type="text"
            placeholder="key1=value1; key2=value2"
            class="input-field"
            :disabled="downloadStore.isDownloading"
          />
        </div>

        <div class="flex items-center space-x-2">
          <input
            v-model="autoClear"
            type="checkbox"
            id="autoClear"
            class="w-4 h-4 text-primary bg-dark-300 border-white/10 rounded focus:ring-primary"
            :disabled="downloadStore.isDownloading"
          />
          <label for="autoClear" class="text-sm text-gray-300">
            下载完成后自动清理临时文件
          </label>
        </div>

        <div class="border-t border-white/10 pt-4 mt-4">
          <div class="flex items-center space-x-2 mb-4">
            <input
              v-model="enableWebDAV"
              type="checkbox"
              id="enableWebDAV"
              class="w-4 h-4 text-primary bg-dark-300 border-white/10 rounded focus:ring-primary"
              :disabled="downloadStore.isDownloading"
            />
            <label for="enableWebDAV" class="text-sm font-medium text-gray-300">
              启用 WebDAV 中转
            </label>
          </div>

          <div v-if="enableWebDAV" class="space-y-4 pl-6 border-l-2 border-white/10">
            <div>
              <label class="block text-sm font-medium text-gray-300 mb-2">
                WebDAV 地址
              </label>
              <input
                v-model="webDAVURL"
                type="text"
                placeholder="https://your-webdav-server.com/dav"
                class="input-field"
                :disabled="downloadStore.isDownloading"
              />
            </div>

            <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <label class="block text-sm font-medium text-gray-300 mb-2">
                  用户名
                </label>
                <input
                  v-model="webDAVUsername"
                  type="text"
                  placeholder="username"
                  class="input-field"
                  :disabled="downloadStore.isDownloading"
                />
              </div>

              <div>
                <label class="block text-sm font-medium text-gray-300 mb-2">
                  密码
                </label>
                <input
                  v-model="webDAVPassword"
                  type="password"
                  placeholder="password"
                  class="input-field"
                  :disabled="downloadStore.isDownloading"
                />
              </div>
            </div>

            <div>
              <label class="block text-sm font-medium text-gray-300 mb-2">
                远程目录 (可选)
              </label>
              <input
                v-model="webDAVRemoteDir"
                type="text"
                placeholder="/videos/movies"
                class="input-field"
                :disabled="downloadStore.isDownloading"
              />
            </div>

            <div class="flex items-center space-x-2">
              <input
                v-model="deleteAfterUpload"
                type="checkbox"
                id="deleteAfterUpload"
                class="w-4 h-4 text-primary bg-dark-300 border-white/10 rounded focus:ring-primary"
                :disabled="downloadStore.isDownloading"
              />
              <label for="deleteAfterUpload" class="text-sm text-gray-300">
                上传完成后删除本地文件
              </label>
            </div>
          </div>
        </div>

        <div class="flex flex-wrap gap-3 pt-4">
          <button
            v-if="!downloadStore.isDownloading"
            @click="startDownload"
            :disabled="!canStart"
            class="btn-primary"
          >
            开始下载
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
            @click="downloadStore.retryDownload(downloadStore.currentTask.id)"
            class="btn-primary"
          >
            重试
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
      <h3 class="text-lg font-semibold mb-4">下载进度</h3>

      <div class="space-y-4">
        <div>
          <div class="flex justify-between text-sm text-gray-400 mb-2">
            <span>{{ downloadStore.currentTask.name }}.mp4</span>
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
            <span class="text-gray-400">状态</span>
            <div class="font-medium" :class="{
              'text-primary': downloadStore.currentTask.status === 'downloading',
              'text-blue-400': downloadStore.currentTask.status === 'uploading',
              'text-yellow-400': downloadStore.currentTask.status === 'paused',
              'text-green-400': downloadStore.currentTask.status === 'completed',
              'text-red-400': downloadStore.currentTask.status === 'failed'
            }">
              {{ 
                downloadStore.currentTask.status === 'downloading' ? '正在下载' :
                downloadStore.currentTask.status === 'uploading' ? '正在上传 WebDAV' :
                downloadStore.currentTask.status === 'paused' ? '已暂停' :
                downloadStore.currentTask.status === 'completed' ? '已完成' :
                downloadStore.currentTask.status === 'failed' ? '失败' : 
                downloadStore.currentTask.status
              }}
            </div>
          </div>

          <div>
            <span class="text-gray-400">速度</span>
            <div class="font-mono">{{ downloadStore.currentTask.speed }}</div>
          </div>

          <div>
            <span class="text-gray-400">片段</span>
            <div class="font-mono">
              {{ downloadStore.currentTask.downloadedSegments }} / {{ downloadStore.currentTask.totalSegments }}
            </div>
          </div>

          <div>
            <span class="text-gray-400">URL</span>
            <div class="truncate text-xs" :title="downloadStore.currentTask.url">
              {{ downloadStore.currentTask.url }}
            </div>
          </div>
        </div>

        <div v-if="downloadStore.currentTask.outputPath" class="mt-4 p-3 bg-green-400/10 rounded-lg border border-green-400/30">
          <div class="text-sm text-green-400 font-medium mb-1">下载完成!</div>
          <div class="text-xs text-gray-400 font-mono break-all">
            {{ downloadStore.currentTask.outputPath }}
          </div>
        </div>

        <div v-if="downloadStore.currentTask.error" class="mt-4 p-3 bg-red-400/10 rounded-lg border border-red-400/30">
          <div class="text-sm text-red-400 font-medium mb-1">下载失败</div>
          <div class="text-xs text-gray-400">
            {{ downloadStore.currentTask.error }}
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      <h3 class="text-lg font-semibold mb-4">下载日志</h3>
      <div class="bg-dark-300 rounded-lg p-4 h-64 overflow-y-auto font-mono text-sm">
        <div v-if="downloadStore.logs.length === 0" class="text-gray-500">
          暂无日志
        </div>
        <div
          v-for="(log, index) in downloadStore.logs"
          :key="index"
          class="mb-1"
          :class="{
            'text-primary': log.level === 'info',
            'text-gray-400': log.level === 'debug',
            'text-yellow-400': log.level === 'warn',
            'text-red-400': log.level === 'error'
          }"
        >
          <span class="text-gray-500">{{ log.timestamp }}</span>
          <span class="ml-2">{{ log.message }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
</style>
