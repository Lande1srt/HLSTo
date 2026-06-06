<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import { settingsAPI } from '@/api'
import WebDAVBrowser from './WebDAVBrowser.vue'

const settingsStore = useSettingsStore()

const settings = ref({
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
  defaultReferer: ''
})

const saving = ref(false)
const saveMessage = ref('')

const clearing = ref(false)
const clearMessage = ref('')

const testing = ref(false)
const testMessage = ref('')
const testSuccess = ref(false)

const showBrowser = ref(false)

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

  // 加载自动清理配置
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

    // 保存自动清理配置
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
    defaultReferer: ''
  }
}
</script>

<template>
  <div class="space-y-6">
    <div class="card">
      <h2 class="text-2xl font-bold mb-6 text-primary">设置</h2>

      <div class="space-y-6">
        <div>
          <label class="block text-sm font-medium text-gray-300 mb-2">
            默认线程数: {{ settings.defaultThreadCount }}
          </label>
          <input v-model.number="settings.defaultThreadCount" type="range" min="1" max="100" class="w-full" />
          <p class="text-xs text-gray-500 mt-1">
            更高的线程数可以加快下载速度，但可能会被服务器限制
          </p>
        </div>

        <div>
          <label class="block text-sm font-medium text-gray-300 mb-2">
            默认输出文件名
          </label>
          <input v-model="settings.defaultOutputName" type="text" class="input-field" placeholder="movie" />
          <p class="text-xs text-gray-500 mt-1">
            不需要包含文件扩展名（.mp4）
          </p>
        </div>

        <div>
          <label class="block text-sm font-medium text-gray-300 mb-2">
            默认保存路径
          </label>
          <input v-model="settings.defaultSavePath" type="text" class="input-field" placeholder="默认当前目录" />
          <p class="text-xs text-gray-500 mt-1">
            留空表示使用程序运行目录
          </p>
        </div>

        <div>
          <label class="block text-sm font-medium text-gray-300 mb-2">
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
          <label class="block text-sm font-medium text-gray-300 mb-2">
            默认主机名 (Referer)
          </label>
          <input v-model="settings.defaultReferer" type="text" class="input-field" placeholder="https://example.com/" />
          <p class="text-xs text-gray-500 mt-1">
            设置默认的 Referer 以绕过部分服务器的鉴权
          </p>
        </div>

        <div class="flex items-center space-x-2">
          <input v-model="settings.autoClear" type="checkbox" id="autoClearSetting"
            class="w-4 h-4 text-primary bg-dark-300 border-white/10 rounded focus:ring-primary" />
          <label for="autoClearSetting" class="text-sm text-gray-300">
            下载完成后自动清理临时文件 (TS 片段)
          </label>
        </div>

        <div class="border-t border-white/10 pt-6 mt-6">
          <h3 class="text-lg font-semibold mb-4 text-gray-200">WebDAV 中转设置</h3>

          <div class="flex items-center space-x-2 mb-4">
            <input v-model="settings.enableWebDAV" type="checkbox" id="enableWebDAVSetting"
              class="w-4 h-4 text-primary bg-dark-300 border-white/10 rounded focus:ring-primary" />
            <label for="enableWebDAVSetting" class="text-sm font-medium text-gray-300">
              启用 WebDAV 中转
            </label>
          </div>

          <div v-if="settings.enableWebDAV" class="space-y-4 pl-6 border-l-2 border-white/10">
            <div>
              <label class="block text-sm font-medium text-gray-300 mb-2">
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
                <label class="block text-sm font-medium text-gray-300 mb-2">
                  用户名
                </label>
                <input v-model="settings.webDAVUsername" type="text" class="input-field" placeholder="username" />
              </div>

              <div>
                <label class="block text-sm font-medium text-gray-300 mb-2">
                  密码
                </label>
                <input v-model="settings.webDAVPassword" type="password" class="input-field" placeholder="password" />
              </div>
            </div>

            <div>
              <label class="block text-sm font-medium text-gray-300 mb-2">
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

            <div class="flex items-center gap-4 pt-2">
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
              <span v-if="testMessage" class="text-xs" :class="testSuccess ? 'text-green-400' : 'text-red-400'">
                {{ testMessage }}
              </span>
            </div>

            <div class="flex items-center space-x-2">
              <input v-model="settings.deleteAfterUpload" type="checkbox" id="deleteAfterUploadSetting"
                class="w-4 h-4 text-primary bg-dark-300 border-white/10 rounded focus:ring-primary" />
              <label for="deleteAfterUploadSetting" class="text-sm text-gray-300">
                上传完成后删除本地文件
              </label>
            </div>
          </div>
        </div>

        <div class="flex items-center gap-3 pt-4">
          <button @click="save" :disabled="saving" class="btn-primary">
            {{ saving ? '保存中...' : '保存设置' }}
          </button>

          <button @click="reset" class="btn-secondary">
            重置
          </button>

          <span v-if="saveMessage" class="text-sm"
            :class="saveMessage.includes('成功') ? 'text-green-400' : 'text-red-400'">
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
          <label class="block text-sm font-medium text-gray-300 mb-2">
            清除下载缓存
          </label>
          <div class="flex items-center gap-4">
            <button @click="clearCache" :disabled="clearing"
              class="btn-secondary text-red-400 border-red-400/30 hover:bg-red-400/10">
              <svg v-if="clearing" class="animate-spin h-4 w-4 mr-2 inline" xmlns="http://www.w3.org/2000/svg"
                fill="none" viewBox="0 0 24 24">
                <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
                <path class="opacity-75" fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z">
                </path>
              </svg>
              立即清除所有缓存目录
            </button>
            <span v-if="clearMessage" class="text-sm text-gray-400 italic">
              {{ clearMessage }}
            </span>
          </div>
          <p class="text-xs text-gray-500 mt-2">
            这将删除当前程序目录下所有以 "download_" 开头的临时文件夹。请确保没有正在进行的下载任务。
          </p>
        </div>
      </div>
      <div class="border-t border-white/10 pt-6 mt-6">
        <div class="flex items-center justify-between mb-4">
          <h3 class="text-lg font-semibold text-gray-200">自动清理缓存</h3>
          <label class="relative inline-flex items-center cursor-pointer">
            <input type="checkbox" v-model="cleanupConfig.enabled" class="sr-only peer">
            <div
              class="w-11 h-6 bg-dark-400 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary">
            </div>
            <span class="ml-3 text-sm font-medium text-gray-300">{{ cleanupConfig.enabled ? '已启用' : '已禁用' }}</span>
          </label>
        </div>

        <div v-if="cleanupConfig.enabled" class="mt-4 space-y-4">
            <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <label class="block text-sm font-medium text-gray-300 mb-2">清理间隔数值</label>
                <input
                  v-model.number="cleanupConfig.interval"
                  type="number"
                  min="1"
                  class="input-field"
                />
              </div>
              <div>
                <label class="block text-sm font-medium text-gray-300 mb-2">间隔单位</label>
                <select v-model="cleanupConfig.unit" class="input-field">
                  <option value="minute">分钟</option>
                  <option value="hour">小时</option>
                  <option value="day">天</option>
                </select>
              </div>
            </div>

            <div class="bg-dark-400/50 rounded-lg p-3 flex flex-wrap gap-4 text-xs">
              <div class="flex items-center gap-2">
                <span class="text-gray-500 text-[10px] uppercase font-bold tracking-wider">上次执行:</span>
                <span class="text-gray-300">{{ formatDate(cleanupConfig.lastRun) }}</span>
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
    </div>

    <div class="card">
      <h3 class="text-lg font-semibold mb-4">关于</h3>
      <div class="text-sm text-gray-400 space-y-2">
        <p>HLSTo<strong class="text-white">M3U8 Downloader</strong> Web UI 版本</p>
        <p>基于<a href="https://github.com/llychao/m3u8-downloader/" target="_blank" rel="noopener noreferrer" style="color: #007bff;text-decoration: none;">m3u8-downloader</a>
          项目开发的多线程 m3u8 视频下载器服务端</p>
        <p>本项目github仓库：<a href="https://github.com/Lande1srt/HLSTo" target="_blank" rel="noopener noreferrer" style="color: #007bff;text-decoration: none;">https://github.com/Lande1srt/HLSTo</a></p>
        <p>本项目发布官网：<a href="https://coldsea.vip/" target="_blank" rel="noopener noreferrer" style="color: #007bff;text-decoration: none;">https://coldsea.vip/</a></p>
        <p class="pt-2 border-t border-white/10 mt-4">
          技术栈: Vue 3 + Vite + TailwindCSS + Go
        </p>
      </div>
    </div>
  </div>
</template>

<style scoped></style>
