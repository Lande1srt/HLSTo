<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useSettingsStore, type Settings } from '@/stores/settings'
import { useDarkModeStore } from '@/stores/darkMode'
import { settingsAPI } from '@/api'
import WebDAVBrowser from './WebDAVBrowser.vue'

const settingsStore = useSettingsStore()
const darkModeStore = useDarkModeStore()

const settings = ref<Settings>({
  defaultThreadCount: 24,
  defaultOutputName: 'movie',
  defaultSavePath: '',
  autoClear: true,
  hostType: 'v1',
  enableWebDAV: false,
  webDAVURL: '',
  webDAVUsername: '',
  webDAVPassword: '',
  webDAVRemoteDir: '',
  deleteAfterUpload: false,
  taskSortOrder: 'desc',
  defaultReferer: '',
  downloadConcurrency: 1,
  mergeConcurrency: 1,
  uploadConcurrency: 1,
  singleMode: false,
  enablePreDownloadCheck: true,
  minFreeSpaceMB: 500,
  diskRefreshInterval: 10
})

const saving = ref(false)
const saveMessage = ref('')

const clearing = ref(false)
const clearMessage = ref('')

const testing = ref(false)
const testMessage = ref('')
const testSuccess = ref(false)

const showBrowser = ref(false)

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
  settings.value.defaultReferer = normalizeReferrer(settings.value.defaultReferer)
}

const cleanupConfig = ref({
  enabled: false,
  interval: 1,
  unit: 'day',
  lastRun: '',
  nextRun: ''
})

const formatDate = (dateStr: string) => {
  if (!dateStr || dateStr.startsWith('0001')) return '从未执行'
  const date = new Date(dateStr)
  return date.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
}

onMounted(async () => {
  await settingsStore.loadSettings()
  settings.value = { ...settingsStore.settings }

  try {
    const res = await settingsAPI.getCleanupConfig()
    if (res.data.code === 200) {
      cleanupConfig.value = res.data.data
    }
  } catch (error) {
    console.error('Failed to load cleanup config:', error)
  }
})

const testConnection = async () => {
  if (!settings.value.webDAVURL) {
    testMessage.value = '请输入 WebDAV 地址'
    testSuccess.value = false
    return
  }

  testing.value = true
  testMessage.value = ''
  testSuccess.value = false

  try {
    const res = await settingsAPI.testWebDAV(settings.value)
    if (res.data.code === 200) {
      testMessage.value = '连接测试成功'
      testSuccess.value = true
    } else {
      testMessage.value = '连接失败: ' + res.data.message
      testSuccess.value = false
    }
  } catch (error: any) {
    testMessage.value = '连接错误: ' + (error.response?.data?.message || '网络错误')
    testSuccess.value = false
  } finally {
    testing.value = false
  }
}

const onDirSelect = (path: string) => {
  settings.value.webDAVRemoteDir = path
}

const clearCache = async () => {
  if (!confirm('确定要清除所有下载缓存文件夹吗？')) {
    return
  }

  clearing.value = true
  clearMessage.value = ''

  try {
    const res = await settingsAPI.clearCache()
    if (res.data.code === 200) {
      clearMessage.value = res.data.data.message
      setTimeout(() => { clearMessage.value = '' }, 3000)
    } else {
      clearMessage.value = '清除失败: ' + res.data.message
    }
  } catch (error: any) {
    clearMessage.value = '清除出错: ' + (error.response?.data?.message || '网络错误')
  } finally {
    clearing.value = false
  }
}

const save = async () => {
  saving.value = true
  saveMessage.value = ''

  try {
    const success = await settingsStore.saveSettings(settings.value)
    await settingsAPI.saveCleanupConfig(cleanupConfig.value)

    if (success) {
      saveMessage.value = '设置保存成功'
      setTimeout(() => {
        saveMessage.value = ''
      }, 3000)
    } else {
      saveMessage.value = '设置保存失败'
    }
  } catch (error) {
    saveMessage.value = '设置保存失败'
  } finally {
    saving.value = false
  }
}

const reset = () => {
  settings.value = {
    defaultThreadCount: 24,
    defaultOutputName: 'movie',
    defaultSavePath: '',
    autoClear: true,
    hostType: 'v1',
    enableWebDAV: false,
    webDAVURL: '',
    webDAVUsername: '',
    webDAVPassword: '',
    webDAVRemoteDir: '',
    deleteAfterUpload: false,
    taskSortOrder: 'desc',
    defaultReferer: '',
    downloadConcurrency: 1,
    mergeConcurrency: 1,
    uploadConcurrency: 1,
    singleMode: false,
    enablePreDownloadCheck: true,
    minFreeSpaceMB: 500,
    diskRefreshInterval: 10
  }
}
</script>

<template>
  <div class="space-y-6">
    <div class="card">
      <h2 class="text-2xl font-bold mb-6 text-primary">设置</h2>

      <div class="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-4 mb-6">
        <div class="lg:w-1/2">
          <div class="flex items-center justify-between">
            <label class="block text-sm font-medium text-gray-600 dark:text-gray-300">
              深色模式
            </label>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="darkModeStore.isDark" class="sr-only peer">
              <div
                class="w-11 h-6 bg-gray-300 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary">
              </div>
              <span class="ml-3 text-sm font-medium text-gray-600 dark:text-gray-300">{{ darkModeStore.isDark ? '已启用' : '已禁用' }}</span>
            </label>
          </div>
          <p class="text-xs text-gray-500 mt-1">主题切换立即生效</p>
        </div>

        <div class="lg:w-1/2">
          <label class="block text-sm font-medium text-gray-600 mb-2">
            默认线程数: {{ settings.defaultThreadCount }}
          </label>
          <input v-model.number="settings.defaultThreadCount" type="range" min="1" max="100" class="w-full" />
          <p class="text-xs text-gray-500 mt-1">
            更高的线程数可以加快下载速度，但可能会被服务器限制
          </p>
        </div>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6">
        <div>
          <label class="block text-sm font-medium text-gray-600 mb-2">
            默认输出文件名
          </label>
          <input v-model="settings.defaultOutputName" type="text" class="input-field" placeholder="movie" />
          <p class="text-xs text-gray-500 mt-1">
            不需要包含文件扩展名（.mp4）
          </p>
        </div>

        <div>
          <label class="block text-sm font-medium text-gray-600 mb-2">
            默认保存路径
          </label>
          <input v-model="settings.defaultSavePath" type="text" class="input-field" placeholder="默认当前目录" />
          <p class="text-xs text-gray-500 mt-1">
            留空表示使用程序运行目录
          </p>
        </div>

        <div>
          <label class="block text-sm font-medium text-gray-600 mb-2">
            Host 类型
          </label>
          <select v-model="settings.hostType" class="input-field">
            <option value="v1">
              V1 - 完整路径 (http(s):// + Host + 目录路径)
            </option>
            <option value="v2">
              V2 - 仅域名 (http(s):// + Host)
            </option>
          </select>
          <p class="text-xs text-gray-500 mt-1">
            如果下载失败，尝试切换 Host 类型
          </p>
        </div>

        <div>
          <label class="block text-sm font-medium text-gray-600 mb-2">
            默认主机名 (Referer)
          </label>
          <input v-model="settings.defaultReferer" type="text" class="input-field" placeholder="https://example.com/" @blur="handleReferrerBlur" />
          <p class="text-xs text-gray-500 mt-1">
            设置默认的 Referer 以绕过部分服务器的鉴权
          </p>
        </div>
      </div>

      <div class="space-y-6">
        <div class="flex items-center space-x-2">
          <input v-model="settings.autoClear" type="checkbox" id="autoClearSetting"
            class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded" />
          <label for="autoClearSetting" class="text-sm text-gray-600">
            下载完成后自动清理临时文件 (TS 片段)
          </label>
        </div>

        <div class="border-t border-gray-300 pt-6">
          <h3 class="text-lg font-semibold mb-4 text-gray-800">WebDAV 中转设置</h3>

          <div class="flex items-center space-x-2 mb-4">
            <input v-model="settings.enableWebDAV" type="checkbox" id="enableWebDAVSetting"
              class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded" />
            <label for="enableWebDAVSetting" class="text-sm font-medium text-gray-600">
              启用 WebDAV 中转
            </label>
          </div>

          <div v-if="settings.enableWebDAV" class="space-y-4 pl-6 border-l-2 border-gray-300">
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-2">
                WebDAV 服务器地址
              </label>
              <input v-model="settings.webDAVURL" type="text" class="input-field"
                placeholder="https://your-webdav-server.com/dav" />
              <p class="text-xs text-gray-500 mt-1">
                例如: https://dav.jianguoyun.com/dav/
              </p>
            </div>

            <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <label class="block text-sm font-medium text-gray-600 mb-2">
                  用户名
                </label>
                <input v-model="settings.webDAVUsername" type="text" class="input-field" placeholder="username" />
              </div>

              <div>
                <label class="block text-sm font-medium text-gray-600 mb-2">
                  密码
                </label>
                <input v-model="settings.webDAVPassword" type="password" class="input-field" placeholder="password" />
              </div>
            </div>

            <div>
              <label class="block text-sm font-medium text-gray-600 mb-2">
                远程目录
              </label>
              <div class="flex gap-2">
                <input v-model="settings.webDAVRemoteDir" type="text" class="input-field"
                  placeholder="/videos/movies" />
                <button @click="showBrowser = true" class="btn-secondary whitespace-nowrap px-4 py-2" title="浏览目录">
                  <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none"
                      stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                      <path
                        d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
                    </svg>
                </button>
              </div>
              <p class="text-xs text-gray-500 mt-1">
                上传文件的目标目录，留空表示根目录
              </p>
            </div>

            <div class="flex items-center gap-4">
              <button @click="testConnection" :disabled="testing"
                class="text-sm font-medium text-primary hover:text-primary/80 transition-colors flex items-center gap-1">
                <svg v-if="testing" class="animate-spin h-4 w-4" xmlns="http://www.w3.org/2000/svg" fill="none"
                  viewBox="0 0 24 24">
                  <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
                  <path class="opacity-75" fill="currentColor"
                    d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z">
                  </path>
                </svg>
                {{ testing ? '测试中...' : '测试连接' }}
              </button>
              <span v-if="testMessage" class="text-xs" :class="testSuccess ? 'text-green-500' : 'text-red-500'">
                {{ testMessage }}
              </span>
            </div>

            <div class="flex items-center space-x-2">
              <input v-model="settings.deleteAfterUpload" type="checkbox" id="deleteAfterUploadSetting"
                class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded" />
              <label for="deleteAfterUploadSetting" class="text-sm text-gray-600">
                上传完成后删除本地文件
              </label>
            </div>
          </div>
        </div>

        <div class="border-t border-gray-300 pt-6 mt-6">
          <div class="flex items-center justify-between mb-4">
            <h3 class="text-lg font-semibold text-gray-800">自动清理缓存</h3>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="cleanupConfig.enabled" class="sr-only peer">
              <div
                class="w-11 h-6 bg-gray-300 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary">
              </div>
              <span class="ml-3 text-sm font-medium text-gray-600">{{ cleanupConfig.enabled ? '已启用' : '已禁用' }}</span>
            </label>
          </div>

          <div v-if="cleanupConfig.enabled" class="mt-4 space-y-4">
              <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div>
                  <label class="block text-sm font-medium text-gray-600 mb-2">清理间隔数值</label>
                  <input
                    v-model.number="cleanupConfig.interval"
                    type="number"
                    min="1"
                    class="input-field"
                  />
                </div>
                <div>
                  <label class="block text-sm font-medium text-gray-600 mb-2">间隔单位</label>
                  <select v-model="cleanupConfig.unit" class="input-field">
                    <option value="minute">分钟</option>
                    <option value="hour">小时</option>
                    <option value="day">天</option>
                  </select>
                </div>
              </div>

              <div class="bg-gray-100 rounded-lg p-3 flex flex-wrap gap-4 text-xs">
                <div class="flex items-center gap-2">
                  <span class="text-gray-500 text-[10px] uppercase font-bold tracking-wider">上次执行:</span>
                  <span class="text-gray-600">{{ formatDate(cleanupConfig.lastRun) }}</span>
                </div>
                <div class="flex items-center gap-2">
                  <span class="text-gray-500 text-[10px] uppercase font-bold tracking-wider">下次预计:</span>
                  <span class="text-primary font-medium">{{ formatDate(cleanupConfig.nextRun) }}</span>
                </div>
              </div>
            </div>
            <p class="text-xs text-gray-500 mt-2">
              根据程序启动时间计时。每次达到间隔时间后，系统将自动调用“清理缓存”逻辑移除所有 download_ 开头的临时目录。
            </p>
        </div>

        <div class="border-t border-gray-300 pt-6 mt-6">
          <div class="flex items-center justify-between mb-4">
            <h3 class="text-lg font-semibold text-gray-800">队列控制</h3>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="settings.singleMode" class="sr-only peer">
              <div
                class="w-11 h-6 bg-gray-300 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary">
              </div>
              <span class="ml-3 text-sm font-medium text-gray-600">单状态处理</span>
            </label>
          </div>

          <p class="text-xs text-gray-500 mb-4" v-if="settings.singleMode">
            单状态模式：同时只能存在一个任务处于下载/合并/上传状态，适用于磁盘空间较小的服务器，避免同时下载文件造成空间不足。
          </p>

          <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-2">同时下载数量</label>
              <input
                v-model.number="settings.downloadConcurrency"
                type="number"
                min="1"
                max="10"
                class="input-field"
              />
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-2">同时合并数量</label>
              <input
                v-model.number="settings.mergeConcurrency"
                type="number"
                min="1"
                max="10"
                class="input-field"
              />
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-2">同时上传数量</label>
              <input
                v-model.number="settings.uploadConcurrency"
                type="number"
                min="1"
                max="10"
                class="input-field"
              />
            </div>
          </div>

          <p class="text-xs text-gray-500 mt-4">
            设置每种任务状态的最大并发数。启用单状态处理模式后，此设置将被忽略。
          </p>
        </div>

        <div class="border-t border-gray-300 pt-6 mt-6">
          <div class="flex items-center justify-between mb-4">
            <h3 class="text-lg font-semibold text-gray-800">预下载检查</h3>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="settings.enablePreDownloadCheck" class="sr-only peer">
              <div
                class="w-11 h-6 bg-gray-300 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary">
              </div>
              <span class="ml-3 text-sm font-medium text-gray-600">{{ settings.enablePreDownloadCheck ? '已启用' : '已禁用' }}</span>
            </label>
          </div>

          <p class="text-xs text-gray-500 mb-4">
            启用后，系统将在开始下载前检查磁盘空间。通过下载首个分片并估算总分片大小，如果预期文件大小超过可用空间，则不启动任务。
          </p>

          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-2">最小保留空间 (MB)</label>
              <input
                v-model.number="settings.minFreeSpaceMB"
                type="number"
                min="100"
                max="10000"
                class="input-field"
              />
              <p class="text-xs text-gray-500 mt-1">
                下载完成后至少保留的磁盘空间
              </p>
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-2">磁盘刷新间隔 (秒)</label>
              <input
                v-model.number="settings.diskRefreshInterval"
                type="number"
                min="5"
                max="3600"
                class="input-field"
              />
              <p class="text-xs text-gray-500 mt-1">
                磁盘信息自动刷新间隔，默认10秒，最大3600秒(1小时)
              </p>
            </div>
          </div>

          <div class="mt-4 p-3 bg-gray-100 dark:bg-dark-200 rounded-lg">
            <p class="text-xs text-gray-600 dark:text-gray-400">
              <strong>预下载检查逻辑说明：</strong>
              <br/>1. 启用预下载检查后，系统会在下载前检查磁盘空间
              <br/>2. 通过下载首个分片估算总分片大小，如果预期空间不足则任务进入等待队列
              <br/>3. 预下载分片失败或取消时，会自动清除临时缓存目录
              <br/>4. 磁盘信息会根据设置的间隔自动刷新
            </p>
          </div>
        </div>

        <div class="flex items-center gap-3 pt-6 mt-6 border-t border-gray-300">
          <button @click="save" :disabled="saving" class="btn-primary">
            {{ saving ? '保存中...' : '保存设置' }}
          </button>

          <button @click="reset" class="btn-secondary">
            重置
          </button>

          <span v-if="saveMessage" class="text-sm"
            :class="saveMessage.includes('成功') ? 'text-green-500' : 'text-red-500'">
            {{ saveMessage }}
          </span>
        </div>
      </div>
    </div>

    <WebDAVBrowser :show="showBrowser" :url="settings.webDAVURL" :username="settings.webDAVUsername"
      :password="settings.webDAVPassword" :initialPath="settings.webDAVRemoteDir" @close="showBrowser = false"
      @select="onDirSelect" />

    <div class="card">
      <h3 class="text-lg font-semibold mb-4 text-primary">系统维护</h3>
      <div class="space-y-4">
        <div>
          <label class="block text-sm font-medium text-gray-600 mb-2">
            清除下载缓存
          </label>
          <div class="flex items-center gap-4">
            <button @click="clearCache" :disabled="clearing"
              class="btn-secondary text-red-500">
              <svg v-if="clearing" class="animate-spin h-4 w-4 mr-2 inline" xmlns="http://www.w3.org/2000/svg"
                fill="none" viewBox="0 0 24 24">
                <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
                <path class="opacity-75" fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z">
                </path>
              </svg>
              立即清除所有缓存目录
            </button>
            <span v-if="clearMessage" class="text-sm text-gray-500 italic">
              {{ clearMessage }}
            </span>
          </div>
          <p class="text-xs text-gray-500 mt-2">
            这将删除当前程序目录下所有以 "download_" 开头的临时文件夹。请确保没有正在进行的下载任务。
          </p>
        </div>
      </div>
    </div>

    <div class="card">
      <h3 class="text-lg font-semibold mb-4 text-gray-800 dark:text-gray-100">关于</h3>
      <div class="text-sm text-gray-600 space-y-2">
        <p>HLSTo - <strong class="text-gray-800">M3U8 Downloader</strong> Web UI 版本</p>
        <p>基于 <a href="https://github.com/llychao/m3u8-downloader/" target="_blank" rel="noopener noreferrer" style="color: #3498db;text-decoration: none;">m3u8-downloader</a>
          项目开发的多线程 m3u8 视频下载器服务端</p>
        <p>本项目发布官网：<a href="https://coldsea.vip/" target="_blank" rel="noopener noreferrer" style="color: #3498db;text-decoration: none;">https://coldsea.vip/</a></p>
        <p>本项目github仓库：<a href="https://github.com/Lande1srt/HLSTo" target="_blank" rel="noopener noreferrer" style="color: #3498db;text-decoration: none;">https://github.com/Lande1srt/HLSTo</a></p>
        <p class="pt-2 border-t border-gray-300 mt-4">
          技术栈: Vue 3 + Vite + TailwindCSS + Go
        </p>
      </div>
    </div>
  </div>
</template>

<style scoped></style>
