<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, onBeforeUnmount } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSettingsStore, type Settings } from '@/stores/settings'
import { useDarkModeStore } from '@/stores/darkMode'
import { settingsAPI, apiKeyAPI, ffmpegAPI, type APIKeyInfo, type FFmpegStatus } from '@/api'
import WebDAVBrowser from './WebDAVBrowser.vue'

type SettingsTab = 'basic' | 'postdownload' | 'cleanup' | 'queue' | 'predownload' | 'apikeys'

// 合法标签白名单，用于查询参数合法化（非法值一律回退基础设置）
const settingsTabs: SettingsTab[] = ['basic', 'postdownload', 'cleanup', 'queue', 'predownload', 'apikeys']
const resolveTab = (value: unknown): SettingsTab =>
  typeof value === 'string' && (settingsTabs as string[]).includes(value) ? (value as SettingsTab) : 'basic'

const route = useRoute()
const router = useRouter()

// 初始化定位：优先读取 URL 查询参数 ?tab=xxx，刷新页面后仍停留在当前标签
const activeTab = ref<SettingsTab>(resolveTab(route.query.tab))

// 初始 URL 携带非法 tab 时：页面回退基础设置，并同步修正地址栏（初始赋值不会触发 watch）
if (route.query.tab != null && resolveTab(route.query.tab) !== route.query.tab) {
  router.replace({ query: { ...route.query, tab: 'basic' } })
}

// 切换标签 -> 同步查询参数（replace 不增加历史栈，刷新、复制链接分享均可保持定位）
watch(activeTab, (tab) => {
  if (resolveTab(route.query.tab) !== tab) {
    router.replace({ query: { ...route.query, tab } })
  }
})

// 浏览器前进/后退或地址栏直接改参数 -> 反向同步标签
watch(() => route.query.tab, (value) => {
  const tab = resolveTab(value)
  if (activeTab.value !== tab) {
    activeTab.value = tab
  }
})
const apiKeys = ref<APIKeyInfo[]>([])
const keyLoading = ref(false)
const showGenerateModal = ref(false)
const newKeyName = ref('')
const newKeyPerms = ref<string[]>(['task:read', 'task:write'])
const newKeyExpiresDays = ref(0)
const generatedKey = ref<{ apiKey: APIKeyInfo; rawKey: string } | null>(null)

const availablePermissions = [
  { key: 'task:read', label: '读取任务', desc: '查看任务列表和详情' },
  { key: 'task:write', label: '任务操作', desc: '启动/暂停/恢复/停止/重试/删除任务' },
  { key: 'settings:read', label: '读取设置', desc: '查看服务器配置' },
  { key: 'settings:write', label: '修改设置', desc: '修改服务器配置' },
  { key: 'disk:read', label: '磁盘信息', desc: '查看磁盘使用情况' }
]

const hasKey = (perm: string) => newKeyPerms.value.includes(perm)
const toggleKey = (perm: string) => {
  const idx = newKeyPerms.value.indexOf(perm)
  if (idx >= 0) {
    newKeyPerms.value.splice(idx, 1)
  } else {
    newKeyPerms.value.push(perm)
  }
}

const loadApiKeys = async () => {
  keyLoading.value = true
  try {
    const res = await apiKeyAPI.list()
    if (res.data.code === 200) {
      apiKeys.value = Array.isArray(res.data.data) ? res.data.data : []
    }
  } catch (error) {
    console.error('Failed to load API keys:', error)
    apiKeys.value = []
  } finally {
    keyLoading.value = false
  }
}

const generateApiKey = async () => {
  if (!newKeyName.value.trim()) {
    alert('请输入密钥名称')
    return
  }
  if (newKeyPerms.value.length === 0) {
    alert('请至少选择一个权限')
    return
  }
  try {
    const res = await apiKeyAPI.generate({
      name: newKeyName.value.trim(),
      permissions: newKeyPerms.value,
      expiresDays: newKeyExpiresDays.value
    })
    if (res.data.code === 200) {
      generatedKey.value = res.data.data
      newKeyName.value = ''
      newKeyPerms.value = ['task:read', 'task:write']
      newKeyExpiresDays.value = 0
      showGenerateModal.value = false
      await loadApiKeys()
    }
  } catch (error: any) {
    alert('生成失败: ' + (error.response?.data?.message || error.message))
  }
}

const revokeApiKey = async (id: string) => {
  if (!confirm('确定要撤销此 API Key 吗？撤销后将无法恢复。')) return
  try {
    const res = await apiKeyAPI.revoke(id)
    if (res.data.code === 200) {
      await loadApiKeys()
    }
  } catch (error) {
    alert('撤销失败')
  }
}

const deleteApiKey = async (id: string) => {
  if (!confirm('确定要永久删除此 API Key 吗？')) return
  try {
    const res = await apiKeyAPI.delete(id)
    if (res.data.code === 200) {
      await loadApiKeys()
    }
  } catch (error) {
    alert('删除失败')
  }
}

const copyToClipboard = (text: string) => {
  navigator.clipboard.writeText(text).then(() => {
    alert('已复制到剪贴板')
  })
}

const permissionLabels: Record<string, string> = {
  'task:read': '读取任务',
  'task:write': '任务操作',
  'settings:read': '读取设置',
  'settings:write': '修改设置',
  'disk:read': '磁盘信息'
}

const formatKeyDate = (dateStr: string) => {
  if (!dateStr) return '-'
  const d = new Date(dateStr)
  return d.toLocaleString('zh-CN')
}

const isKeyExpired = (key: APIKeyInfo) => {
  if (!key.expiresAt) return false
  return new Date(key.expiresAt) < new Date()
}

const settingsStore = useSettingsStore()
const darkModeStore = useDarkModeStore()

const createDefaultSettings = (): Settings => ({
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
  compressConcurrency: 1,
  packConcurrency: 1,
  uploadConcurrency: 1,
  singleMode: false,
  enablePreDownloadCheck: true,
  minFreeSpaceMB: 500,
  diskRefreshInterval: 10,
  mergeAfterDownload: true,
  mergeMethod: 'auto',
  ffmpegMuxMode: 'copy',
  hlsPackEnabled: false,
  hlsEncryptEnabled: false,
  hlsEncryptMode: 'generated',
  hlsKeyURL: '',
  hlsPackForm: 'multi',
  compressAfterMerge: false,
  compressBitrateThreshold: 2048,
  compressTargetBitrate: 0
})

interface CleanupConfig {
  enabled: boolean
  interval: number
  unit: string
  lastRun: string
  nextRun: string
}

const createDefaultCleanupConfig = (): CleanupConfig => ({
  enabled: false,
  interval: 1,
  unit: 'day',
  lastRun: '',
  nextRun: ''
})

const settings = ref<Settings>(createDefaultSettings())

// WebDAV 上传测速状态
interface SpeedTestLog {
  state: string
  sizeMB: number
  averageKbps: number
  perSecond: string[]
  error?: string
}
const speedTestLog = ref<SpeedTestLog | null>(null)
const speedTestRunning = ref(false)
const speedTestSizeMB = ref(10)
let speedTestTimer: ReturnType<typeof setInterval> | null = null

const speedTestLines = computed(() =>
  speedTestLog.value ? (speedTestLog.value.perSecond || []).join('\n') : ''
)
const speedTestAverage = computed(() => speedTestLog.value?.averageKbps || 0)
const speedStateText = computed(() => {
  switch (speedTestLog.value?.state) {
    case 'running': return '测速中...'
    case 'done': return '已完成（远端文件已清理）'
    case 'stopped': return '已手动中断（远端文件保留）'
    case 'failed': return '测速失败'
    default: return ''
  }
})
const speedStateClass = computed(() => {
  switch (speedTestLog.value?.state) {
    case 'running': return 'text-blue-500'
    case 'done': return 'text-green-600'
    case 'stopped': return 'text-amber-600'
    case 'failed': return 'text-red-500'
    default: return ''
  }
})

const formatRate = (kbps: number) =>
  kbps >= 1024 ? `${(kbps / 1024).toFixed(2)} MB/s` : `${Math.round(kbps)} KB/s`

const pollSpeedTest = async () => {
  try {
    const res = await settingsAPI.getSpeedTestLog()
    if (res.data.code === 200 && res.data.data) {
      speedTestLog.value = res.data.data
      const st = res.data.data.state
      if (st !== 'running') {
        speedTestRunning.value = false
        if (speedTestTimer) {
          clearInterval(speedTestTimer)
          speedTestTimer = null
        }
      }
    }
  } catch (e) {
    console.error('查询测速日志失败', e)
  }
}

const startSpeedTest = async () => {
  if (!settings.value.webDAVURL) {
    saveMessage.value = '请先在 WebDAV 设置中填写地址'
    return
  }
  let sizeMB = Number(speedTestSizeMB.value)
  if (!Number.isFinite(sizeMB) || sizeMB < 1) {
    sizeMB = 10
    speedTestSizeMB.value = 10
  }
  sizeMB = Math.floor(sizeMB)
  try {
    const res = await settingsAPI.startSpeedTest({
      webDAVURL: settings.value.webDAVURL,
      webDAVUsername: settings.value.webDAVUsername,
      webDAVPassword: settings.value.webDAVPassword,
      webDAVRemoteDir: settings.value.webDAVRemoteDir,
      sizeMB
    })
    if (res.data.code === 200) {
      speedTestRunning.value = true
      await pollSpeedTest()
      if (speedTestTimer) clearInterval(speedTestTimer)
      speedTestTimer = setInterval(pollSpeedTest, 1000)
    }
  } catch (e: any) {
    saveMessage.value = e?.response?.data?.message || '启动测速失败'
  }
}

const stopSpeedTest = async () => {
  try {
    await settingsAPI.stopSpeedTest()
  } catch (e) {
    console.error('中断测速失败', e)
  }
}

onBeforeUnmount(() => {
  if (speedTestTimer) clearInterval(speedTestTimer)
})

// 进入页面时加载上次测速记录
pollSpeedTest()


const saving = ref(false)
const saveMessage = ref('')

const clearing = ref(false)
const clearMessage = ref('')

const testing = ref(false)
const testMessage = ref('')
const testSuccess = ref(false)

const showBrowser = ref(false)

// ---- FFmpeg 环境检测与私有安装 ----
const ffmpegStatus = ref<FFmpegStatus | null>(null)
let ffmpegTimer: ReturnType<typeof setInterval> | null = null

const ffmpegSourceLabels: Record<string, string> = {
  env: '环境变量 FFMPEG_PATH',
  system: '系统 PATH',
  private: '私有目录'
}

const stopFFmpegPolling = () => {
  if (ffmpegTimer !== null) {
    clearInterval(ffmpegTimer)
    ffmpegTimer = null
  }
}

const startFFmpegPolling = () => {
  stopFFmpegPolling()
  ffmpegTimer = setInterval(() => {
    ffmpegAPI.status()
      .then((res) => {
        if (res.data.code === 200) {
          ffmpegStatus.value = res.data.data
          if (!res.data.data.installing) {
            stopFFmpegPolling()
          }
        }
      })
      .catch((error) => console.error('FFmpeg 状态轮询失败:', error))
  }, 1500)
}

const loadFFmpegStatus = async (forceRefresh = false) => {
  try {
    const res = await ffmpegAPI.status(forceRefresh)
    if (res.data.code === 200) {
      ffmpegStatus.value = res.data.data
      if (res.data.data.installing) {
        startFFmpegPolling()
      }
    }
  } catch (error) {
    console.error('加载 FFmpeg 状态失败:', error)
  }
}

const installFFmpeg = async () => {
  try {
    const res = await ffmpegAPI.install()
    if (res.data.code === 200) {
      ffmpegStatus.value = res.data.data
      startFFmpegPolling()
    }
  } catch (error: any) {
    alert('启动安装失败: ' + (error.response?.data?.message || error.message))
  }
}

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

const cleanupConfig = ref<CleanupConfig>(createDefaultCleanupConfig())

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
  // 与默认值合并兜底，防止后端返回的 settings 存在缺失字段
  settings.value = { ...createDefaultSettings(), ...settingsStore.settings }

  try {
    const res = await settingsAPI.getCleanupConfig()
    if (res.data.code === 200 && res.data.data) {
      // 合并到默认值对象，避免后端返回字段缺失导致 undefined
      cleanupConfig.value = { ...createDefaultCleanupConfig(), ...res.data.data }
    }
  } catch (error) {
    console.error('Failed to load cleanup config:', error)
  }

  loadApiKeys()
  loadFFmpegStatus()
})

onUnmounted(() => {
  stopFFmpegPolling()
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

  // 客户端校验：仅 FFmpeg 合并与二次 HLS 分片都需要 FFmpeg
  const needFFmpeg = settings.value.mergeMethod === 'ffmpeg' || settings.value.hlsPackEnabled
  if (needFFmpeg && !ffmpegStatus.value?.available) {
    saveMessage.value = '该设置要求先检测到 FFmpeg 环境，请先检测或私有安装'
    saving.value = false
    return
  }

  // 加密以二次 HLS 分片开启为前提
  if (settings.value.hlsEncryptEnabled && !settings.value.hlsPackEnabled) {
    saveMessage.value = 'HLS 加密需要先开启二次 HLS 分片'
    saving.value = false
    return
  }

  // 指定密钥必须填写 URL
  if (settings.value.hlsEncryptEnabled && settings.value.hlsEncryptMode === 'specified' &&
    !settings.value.hlsKeyURL.trim()) {
    saveMessage.value = '指定密钥模式必须填写 key 文件 URL'
    saving.value = false
    return
  }

  // 合法化字段
  if (!['auto', 'ffmpeg', 'gomedia'].includes(settings.value.mergeMethod)) {
    settings.value.mergeMethod = 'auto'
  }
  if (!['copy', 'h264', 'h265'].includes(settings.value.ffmpegMuxMode)) {
    settings.value.ffmpegMuxMode = 'copy'
  }
  if (!['generated', 'specified'].includes(settings.value.hlsEncryptMode)) {
    settings.value.hlsEncryptMode = 'generated'
  }
  if (!['multi', 'single'].includes(settings.value.hlsPackForm)) {
    settings.value.hlsPackForm = 'multi'
  }
  if (!Number.isFinite(settings.value.compressBitrateThreshold) || settings.value.compressBitrateThreshold < 1) {
    settings.value.compressBitrateThreshold = 2048
  }
  if (!Number.isFinite(settings.value.compressTargetBitrate) || settings.value.compressTargetBitrate < 0) {
    settings.value.compressTargetBitrate = 0
  }

  // 各阶段并发数合法化：非有限值或 <1（空输入/0/负数）时兜底为 1
  const clampConcurrency = (v: number): number =>
    Number.isFinite(v) && v >= 1 ? Math.floor(v) : 1
  settings.value.downloadConcurrency = clampConcurrency(settings.value.downloadConcurrency)
  settings.value.mergeConcurrency = clampConcurrency(settings.value.mergeConcurrency)
  settings.value.compressConcurrency = clampConcurrency(settings.value.compressConcurrency)
  settings.value.packConcurrency = clampConcurrency(settings.value.packConcurrency)
  settings.value.uploadConcurrency = clampConcurrency(settings.value.uploadConcurrency)

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
  // 使用工厂函数创建全新默认对象，避免引用共享导致的污染
  settings.value = createDefaultSettings()
  cleanupConfig.value = createDefaultCleanupConfig()
  // 清除旧的提示信息
  saveMessage.value = ''
  testMessage.value = ''
  clearMessage.value = ''
}
</script>

<template>
  <div class="space-y-6">
    <div class="card">
      <h2 class="text-2xl font-bold mb-4 text-primary">设置</h2>

      <div class="flex flex-wrap gap-x-1 border-b border-gray-300 dark:border-gray-600 mb-6">
        <button
          @click="activeTab = 'basic'"
          :class="['px-3 py-2 text-sm font-medium transition-colors whitespace-nowrap',
            activeTab === 'basic'
              ? 'text-primary border-b-2 border-primary'
              : 'text-gray-500 hover:text-gray-700 dark:hover:text-gray-300']"
        >基础设置</button>
        <button
          @click="activeTab = 'postdownload'"
          :class="['px-3 py-2 text-sm font-medium transition-colors whitespace-nowrap',
            activeTab === 'postdownload'
              ? 'text-primary border-b-2 border-primary'
              : 'text-gray-500 hover:text-gray-700 dark:hover:text-gray-300']"
        >下载完成动作</button>
        <button
          @click="activeTab = 'cleanup'"
          :class="['px-3 py-2 text-sm font-medium transition-colors whitespace-nowrap',
            activeTab === 'cleanup'
              ? 'text-primary border-b-2 border-primary'
              : 'text-gray-500 hover:text-gray-700 dark:hover:text-gray-300']"
        >自动清理</button>
        <button
          @click="activeTab = 'queue'"
          :class="['px-3 py-2 text-sm font-medium transition-colors whitespace-nowrap',
            activeTab === 'queue'
              ? 'text-primary border-b-2 border-primary'
              : 'text-gray-500 hover:text-gray-700 dark:hover:text-gray-300']"
        >队列控制</button>
        <button
          @click="activeTab = 'predownload'"
          :class="['px-3 py-2 text-sm font-medium transition-colors whitespace-nowrap',
            activeTab === 'predownload'
              ? 'text-primary border-b-2 border-primary'
              : 'text-gray-500 hover:text-gray-700 dark:hover:text-gray-300']"
        >预下载检查</button>
        <button
          @click="activeTab = 'apikeys'"
          :class="['px-3 py-2 text-sm font-medium transition-colors whitespace-nowrap',
            activeTab === 'apikeys'
              ? 'text-primary border-b-2 border-primary'
              : 'text-gray-500 hover:text-gray-700 dark:hover:text-gray-300']"
        >API 密钥管理</button>
      </div>

      <!-- Tab：基础设置 -->
      <div v-if="activeTab === 'basic'" class="space-y-6">
        <div class="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-4 mb-2">
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

          <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
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

          <div class="flex items-center space-x-2 pt-2">
            <input v-model="settings.autoClear" type="checkbox" id="autoClearSetting"
              class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded" />
            <label for="autoClearSetting" class="text-sm text-gray-600">
              下载完成后自动清理临时文件 (TS 片段)
            </label>
          </div>
        </div>

      <!-- Tab：下载完成动作（合并 + 二次 HLS 分片 + WebDAV 中转） -->
      <div v-else-if="activeTab === 'postdownload'" class="space-y-10">

        <!-- 动作一：合并 -->
        <section class="space-y-4">
          <div>
            <h4 class="text-sm font-semibold text-gray-800 dark:text-gray-100">合并</h4>
            <p class="text-xs text-gray-500 mt-1">
              下载完成后是否合并 TS 分片以及使用的合并方式；点击底部「保存设置」后生效。
            </p>
          </div>

          <!-- FFmpeg 环境状态提示 -->
          <div v-if="ffmpegStatus?.installing" class="text-xs text-primary">
            FFmpeg 正在私有安装中，安装完成后即可使用 FFmpeg 合并。
          </div>
          <div v-else-if="!ffmpegStatus?.available"
            class="flex flex-wrap items-center gap-2 p-3 rounded-lg bg-yellow-50 border border-yellow-200 dark:bg-yellow-900/20 dark:border-yellow-800">
            <span class="text-xs text-yellow-800 dark:text-yellow-200">
              未检测到 FFmpeg：FFmpeg 合并与二次 HLS 分片不可用，将使用内置 Go 逻辑合并。可在下方「FFmpeg 环境」卡片私有安装。
            </span>
            <button @click="loadFFmpegStatus(true)" class="text-xs text-primary underline">重新检测</button>
          </div>

          <!-- 合并总开关 -->
          <div class="space-y-1">
            <div class="flex items-center space-x-2">
              <input v-model="settings.mergeAfterDownload" type="checkbox" id="mergeAfterDownloadSetting"
                class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded" />
              <label for="mergeAfterDownloadSetting"
                class="text-sm font-medium text-gray-700 dark:text-gray-200">
                下载完成后合并
              </label>
            </div>
            <p class="text-xs pl-6 text-gray-500">
              关闭后保留原始 TS 分片目录作为最终产物，任务直接完成（不再执行二次 HLS 分片与上传）。
            </p>
          </div>

          <!-- 合并方式与编码模式 -->
          <div v-if="settings.mergeAfterDownload"
            class="space-y-4 pl-4 md:pl-6 border-l-2 border-gray-200 dark:border-gray-700">
            <div class="space-y-3">
              <label class="block text-sm font-medium text-gray-600 mb-1">合并方式</label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.mergeMethod" type="radio" value="auto"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  自动（默认）
                  <span class="block text-xs text-gray-500">优先使用检测到的 FFmpeg，不可用或失败时兜底 Go 逻辑</span>
                </span>
              </label>

              <label class="flex items-start gap-2 cursor-pointer"
                :class="{ 'opacity-50': !ffmpegStatus?.available }">
                <input v-model="settings.mergeMethod" type="radio" value="ffmpeg"
                  :disabled="!ffmpegStatus?.available"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  仅 FFmpeg
                  <span class="block text-xs text-gray-500">只使用 FFmpeg，失败不回退 Go 逻辑</span>
                </span>
              </label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.mergeMethod" type="radio" value="gomedia"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  仅 Go
                  <span class="block text-xs text-gray-500">内置 gomedia 逻辑，无外部依赖，对 H.265 等编码兼容性可能不佳</span>
                </span>
              </label>
            </div>

            <!-- FFmpeg 编码模式（仅 FFmpeg 处理时生效） -->
            <div v-if="settings.mergeMethod !== 'gomedia'" class="space-y-3">
              <label class="block text-sm font-medium text-gray-600 mb-1">编码模式</label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.ffmpegMuxMode" type="radio" value="copy"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  源流式复制
                  <span class="block text-xs text-gray-500">不重新编码（-c copy），速度最快、零画质损失</span>
                </span>
              </label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.ffmpegMuxMode" type="radio" value="h264"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  转码为 H.264
                  <span class="block text-xs text-gray-500">libx264 编码，设备兼容性最广，耗时较长</span>
                </span>
              </label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.ffmpegMuxMode" type="radio" value="h265"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  转码为 H.265
                  <span class="block text-xs text-gray-500">libx265 编码，文件体积更小，耗时最长</span>
                </span>
              </label>
            </div>
          </div>
        </section>

        <hr class="border-gray-200 dark:border-gray-700" />

        <!-- 动作二：二次 HLS 分片 -->
        <section class="space-y-4">
          <div>
            <h4 class="text-sm font-semibold text-gray-800 dark:text-gray-100">二次 HLS 分片</h4>
            <p class="text-xs text-gray-500 mt-1">
              合并产物再次切片为 HLS（FFmpeg hls muxer，-c copy 不重编码），可选多文件目录或单文件 tsbin。
            </p>
          </div>

          <!-- 二次分片开关 -->
          <div class="space-y-1">
            <div class="flex items-center space-x-2">
              <input v-model="settings.hlsPackEnabled" type="checkbox" id="hlsPackEnabledSetting"
                :disabled="!settings.mergeAfterDownload || !ffmpegStatus?.available"
                class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded disabled:opacity-50" />
              <label for="hlsPackEnabledSetting"
                class="text-sm font-medium text-gray-700 dark:text-gray-200"
                :class="{ 'opacity-50': !settings.mergeAfterDownload || !ffmpegStatus?.available }">
                开启二次 HLS 分片
              </label>
            </div>
            <p class="text-xs pl-6 text-gray-500">
              <template v-if="!settings.mergeAfterDownload">需先开启「下载完成后合并」。</template>
              <template v-else-if="!ffmpegStatus?.available">需检测到 FFmpeg 环境。</template>
              <template v-else>开启后可在下方选择多文件目录或单文件 tsbin 形态。</template>
            </p>
          </div>

          <!-- 产物形态 + 加密设置 -->
          <div v-if="settings.hlsPackEnabled"
            class="space-y-4 pl-4 md:pl-6 border-l-2 border-gray-200 dark:border-gray-700">
            <!-- 产物形态 -->
            <div class="space-y-3">
              <label class="block text-sm font-medium text-gray-600 dark:text-gray-300">产物形态</label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.hlsPackForm" type="radio" value="multi"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  多文件目录
                  <span class="block text-xs text-gray-500">index.m3u8 + 多个 seg 分片，逐文件上传（默认）</span>
                </span>
              </label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.hlsPackForm" type="radio" value="single"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  单文件 tsbin
                  <span class="block text-xs text-gray-500">
                    分片二进制组合为一个 tsbin，m3u8 以 EXT-X-BYTERANGE + Range 请求定位；单文件上传避免分片丢失，同目录附 meta.json
                  </span>
                </span>
              </label>
            </div>

            <div class="space-y-1">
              <div class="flex items-center space-x-2">
                <input v-model="settings.hlsEncryptEnabled" type="checkbox" id="hlsEncryptEnabledSetting"
                  class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded" />
                <label for="hlsEncryptEnabledSetting"
                  class="text-sm font-medium text-gray-700 dark:text-gray-200">
                  启用 AES-128 加密
                </label>
              </div>
              <p class="text-xs pl-6 text-gray-500">
                FFmpeg 通过 EXT-X-KEY 对分片执行 AES-128 加密；密钥固定 16 字节，随任务保存，可在任务列表下载。
              </p>
            </div>

            <div v-if="settings.hlsEncryptEnabled" class="space-y-3">
              <label class="block text-sm font-medium text-gray-600 mb-1">密钥来源</label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.hlsEncryptMode" type="radio" value="generated"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  生成独立文件密钥
                  <span class="block text-xs text-gray-500">每个任务随机生成 16 字节密钥，任务之间相互独立</span>
                </span>
              </label>

              <label class="flex items-start gap-2 cursor-pointer">
                <input v-model="settings.hlsEncryptMode" type="radio" value="specified"
                  class="w-4 h-4 mt-0.5 text-primary" />
                <span class="text-sm text-gray-700 dark:text-gray-200">
                  指定密钥
                  <span class="block text-xs text-gray-500">从给定 URL 下载 key 文件后执行加密；下载后缓存，相同 URL 复用</span>
                </span>
              </label>

              <div v-if="settings.hlsEncryptMode === 'specified'">
                <label class="block text-sm font-medium text-gray-600 mb-1">Key 文件 URL</label>
                <input v-model="settings.hlsKeyURL" type="text"
                  placeholder="https://example.com/path/to/enc.key"
                  class="input-field" />
                <p class="text-xs text-gray-500 mt-1">
                  URL 指向的文件必须为 16 字节的原始密钥（非文本口令）。
                </p>
              </div>
            </div>
          </div>
        </section>

        <hr class="border-gray-200 dark:border-gray-700" />

        <!-- 动作二点五：高码率压缩 -->
        <section class="space-y-4">
          <div>
            <h4 class="text-sm font-semibold text-gray-800 dark:text-gray-100">高码率压缩</h4>
            <p class="text-xs text-gray-500 mt-1">
              合并完成后解析成片总码率，超过阈值时调用 FFmpeg 重新编码压缩，再执行后续步骤。
            </p>
          </div>

          <div class="space-y-1">
            <div class="flex items-center space-x-2">
              <input v-model="settings.compressAfterMerge" type="checkbox" id="compressAfterMergeSetting"
                class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded" />
              <label for="compressAfterMergeSetting"
                class="text-sm font-medium text-gray-700 dark:text-gray-200">
                启用高码率压缩
              </label>
            </div>
            <p class="text-xs pl-6 text-gray-500">默认关闭；压缩会重新编码，耗时较长，失败时保留原文件。</p>
          </div>

          <div v-if="settings.compressAfterMerge" class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-1">触发阈值（kbps）</label>
              <input v-model.number="settings.compressBitrateThreshold" type="number" min="1"
                class="input-field" />
              <p class="text-xs text-gray-500 mt-1">成片总码率高于此值才压缩，默认 2048。</p>
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-1">目标码率（kbps）</label>
              <input v-model.number="settings.compressTargetBitrate" type="number" min="0"
                class="input-field" />
              <p class="text-xs text-gray-500 mt-1">留空或 0 表示压缩到阈值本身。</p>
            </div>
          </div>
        </section>

        <hr class="border-gray-200 dark:border-gray-700" />

        <!-- 动作三：WebDAV 中转 -->
        <section class="space-y-4">
          <div>
            <h4 class="text-sm font-semibold text-gray-800 dark:text-gray-100">WebDAV 中转</h4>
            <p class="text-xs text-gray-500 mt-1">封装完成后的上传动作。</p>
          </div>

          <div class="flex items-center space-x-2 mb-2">
            <input v-model="settings.enableWebDAV" type="checkbox" id="enableWebDAVSetting"
              class="w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded" />
            <label for="enableWebDAVSetting" class="text-sm font-medium text-gray-700 dark:text-gray-200">
              启用 WebDAV 中转
            </label>
          </div>

          <div v-if="settings.enableWebDAV" class="space-y-5 pl-4 md:pl-6 border-l-2 border-gray-200 dark:border-gray-700">
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
        </section>

        <!-- WebDAV 上传测速（紧邻 WebDAV 配置） -->
        <section class="space-y-4">
          <div class="border border-gray-200 dark:border-gray-700 rounded-lg p-4 space-y-3">
            <div class="flex items-center justify-between flex-wrap gap-2">
              <div>
                <h4 class="text-sm font-semibold text-gray-800 dark:text-gray-100">WebDAV 上传测速</h4>
                <p class="text-xs text-gray-500 mt-1">
                  生成随机流上传到当前配置的 WebDAV，观测此服务器的实际上传速度（用于回国测试）。大小可自定义，默认 10MB。
                </p>
              </div>
              <div class="flex items-center gap-2 flex-wrap">
                <label class="flex items-center gap-1 text-xs text-gray-500">
                  大小
                  <input v-model.number="speedTestSizeMB" type="number" min="1"
                    class="input-field w-24 py-1 text-xs" /> MB
                </label>
                <button v-if="!speedTestRunning" type="button" @click="startSpeedTest"
                  class="btn-primary text-sm px-3 py-1.5">开始测速</button>
                <button v-else type="button" @click="stopSpeedTest"
                  class="text-sm px-3 py-1.5 rounded-lg bg-red-500 text-white hover:bg-red-600">中断测速</button>
              </div>
            </div>

            <div v-if="speedTestLog" class="space-y-2">
              <div class="flex items-center gap-2 text-xs">
                <span :class="speedStateClass">{{ speedStateText }}</span>
                <span v-if="speedTestAverage" class="text-gray-500">
                  平均 {{ formatRate(speedTestAverage) }}
                </span>
              </div>
              <pre class="bg-gray-900 text-green-300 text-xs rounded-lg p-3 h-56 overflow-y-auto whitespace-pre-wrap font-mono">{{ speedTestLines }}</pre>
            </div>
          </div>
        </section>
      </div>

      <!-- Tab：自动清理缓存 -->
      <div v-else-if="activeTab === 'cleanup'" class="space-y-4">
          <div class="flex items-center justify-between">
            <h3 class="text-lg font-semibold text-gray-800 dark:text-gray-100">自动清理缓存</h3>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="cleanupConfig.enabled" class="sr-only peer">
              <div
                class="w-11 h-6 bg-gray-300 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary">
              </div>
              <span class="ml-3 text-sm font-medium text-gray-600">{{ cleanupConfig.enabled ? '已启用' : '已禁用' }}</span>
            </label>
          </div>

          <div v-if="cleanupConfig.enabled" class="space-y-4">
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

            <div class="bg-gray-100 dark:bg-dark-200 rounded-lg p-3 flex flex-wrap gap-4 text-xs">
              <div class="flex items-center gap-2">
                <span class="text-gray-500 text-[10px] uppercase font-bold tracking-wider">上次执行:</span>
                <span class="text-gray-600 dark:text-gray-300">{{ formatDate(cleanupConfig.lastRun) }}</span>
              </div>
              <div class="flex items-center gap-2">
                <span class="text-gray-500 text-[10px] uppercase font-bold tracking-wider">下次预计:</span>
                <span class="text-primary font-medium">{{ formatDate(cleanupConfig.nextRun) }}</span>
              </div>
            </div>
          </div>

          <p class="text-xs text-gray-500 mt-2">
            根据程序启动时间计时。每次达到间隔时间后，系统将自动调用「清理缓存」逻辑移除所有 download_ 开头的临时目录。
          </p>
        </div>

      <!-- Tab：队列控制 -->
      <div v-else-if="activeTab === 'queue'" class="space-y-4">
          <div class="flex items-center justify-between">
            <h3 class="text-lg font-semibold text-gray-800 dark:text-gray-100">队列控制</h3>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="settings.singleMode" class="sr-only peer">
              <div
                class="w-11 h-6 bg-gray-300 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary">
              </div>
              <span class="ml-3 text-sm font-medium text-gray-600">单状态处理</span>
            </label>
          </div>

          <p class="text-xs text-gray-500 mb-2" v-if="settings.singleMode">
            单状态模式：同时只能存在一个任务处于下载/合并/压缩/分片/上传状态，适用于磁盘空间较小的服务器，避免同时下载文件造成空间不足。
          </p>

          <div class="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-5 gap-4">
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
              <label class="block text-sm font-medium text-gray-600 mb-2" title="FFmpeg 码率压缩，CPU 密集，建议保持 1">同时压缩数量</label>
              <input
                v-model.number="settings.compressConcurrency"
                type="number"
                min="1"
                max="10"
                class="input-field"
              />
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-600 mb-2" title="FFmpeg 二次 HLS 分片，CPU/IO 密集，建议保持 1">同时分片数量</label>
              <input
                v-model.number="settings.packConcurrency"
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

          <p class="text-xs text-gray-500 mt-2">
            设置每个处理阶段的最大并发数。其中「压缩」「分片」依赖 FFmpeg、CPU 消耗高，建议保持为 1，避免多个任务同时转码导致整体变慢，也可防止多任务合并占用过多磁盘空间。启用单状态处理模式后，此设置将被忽略。
          </p>
        </div>

      <!-- Tab：预下载检查 -->
      <div v-else-if="activeTab === 'predownload'" class="space-y-4">
          <div class="flex items-center justify-between">
            <h3 class="text-lg font-semibold text-gray-800 dark:text-gray-100">预下载检查</h3>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="settings.enablePreDownloadCheck" class="sr-only peer">
              <div
                class="w-11 h-6 bg-gray-300 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary">
              </div>
              <span class="ml-3 text-sm font-medium text-gray-600">{{ settings.enablePreDownloadCheck ? '已启用' : '已禁用' }}</span>
            </label>
          </div>

          <p class="text-xs text-gray-500 mb-2">
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

          <div class="mt-2 p-3 bg-gray-100 dark:bg-dark-200 rounded">
            <p class="text-xs text-gray-600 dark:text-gray-400">
              <strong>预下载检查逻辑说明：</strong>
              <br/>1. 启用预下载检查后，系统会在下载前检查磁盘空间
              <br/>2. 通过下载首个分片估算总分片大小，如果预期空间不足则任务进入等待队列
              <br/>3. 预下载分片失败或取消时，会自动清除临时缓存目录
              <br/>4. 磁盘信息会根据设置的间隔自动刷新
            </p>
          </div>
        </div>

      <!-- 保存 / 重置按钮（5个设置Tab显示，API密钥Tab不显示） -->
      <div v-if="activeTab !== 'apikeys'" class="flex items-center gap-3 pt-6 mt-6 border-t border-gray-200 dark:border-gray-700">
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

      <div v-else-if="activeTab === 'apikeys'" class="space-y-6">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-lg font-semibold text-gray-800 dark:text-gray-100">API 密钥</h3>
            <p class="text-sm text-gray-500">管理远程访问密钥，用于 Android 等移动设备连接</p>
          </div>
          <button @click="showGenerateModal = true" class="btn-primary">生成新密钥</button>
        </div>

        <div v-if="keyLoading" class="text-center py-8 text-gray-500">加载中...</div>

        <div v-else-if="apiKeys.length === 0" class="text-center py-8 text-gray-500">
          <p>暂无 API 密钥</p>
          <p class="text-xs mt-2">点击上方按钮创建第一个密钥</p>
        </div>

        <div v-else class="space-y-3">
          <div v-for="key in apiKeys" :key="key.id" class="border border-gray-200 dark:border-gray-700 rounded-lg p-4 hover:shadow-md transition-shadow">
            <div class="flex items-start justify-between">
              <div class="flex-1">
                <div class="flex items-center gap-2 mb-2">
                  <span class="font-medium text-gray-800 dark:text-gray-100">{{ key.name }}</span>
                  <span v-if="!key.isActive" class="px-2 py-0.5 text-xs bg-red-100 text-red-600 rounded">已撤销</span>
                  <span v-else-if="isKeyExpired(key)" class="px-2 py-0.5 text-xs bg-yellow-100 text-yellow-600 rounded">已过期</span>
                  <span v-else class="px-2 py-0.5 text-xs bg-green-100 text-green-600 rounded">活跃</span>
                </div>
                <div class="text-sm text-gray-500 space-y-1">
                  <p>前缀: <code class="bg-gray-100 dark:bg-gray-800 px-1 rounded">{{ key.keyPrefix }}...</code></p>
                  <p>权限: {{ (key.permissions || []).map(p => permissionLabels[p] || p).join(', ') }}</p>
                  <p>创建: {{ formatKeyDate(key.createdAt) }}</p>
                  <p v-if="key.lastUsedAt">最后使用: {{ formatKeyDate(key.lastUsedAt) }}</p>
                  <p v-if="key.expiresAt">过期时间: {{ formatKeyDate(key.expiresAt) }}</p>
                </div>
              </div>
              <div class="flex gap-2">
                <button
                  @click="revokeApiKey(key.id)"
                  :disabled="!key.isActive"
                  class="btn-secondary text-xs px-3 py-1"
                >撤销</button>
                <button @click="deleteApiKey(key.id)" class="btn-secondary text-xs px-3 py-1 text-red-500">删除</button>
              </div>
            </div>
          </div>
        </div>

        <div v-if="showGenerateModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div class="bg-white dark:bg-gray-800 rounded-lg p-6 w-full max-w-md mx-4">
            <h3 class="text-lg font-semibold mb-4">生成 API 密钥</h3>
            
            <div class="space-y-4">
              <div>
                <label class="block text-sm font-medium text-gray-600 mb-1">密钥名称</label>
                <input v-model="newKeyName" type="text" class="input-field" placeholder="例如: 手机APP" />
              </div>

              <div>
                <label class="block text-sm font-medium text-gray-600 mb-2">权限</label>
                <div class="space-y-2">
                  <label v-for="perm in availablePermissions" :key="perm.key" class="flex items-start gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      :checked="hasKey(perm.key)"
                      @change="toggleKey(perm.key)"
                      class="mt-1 w-4 h-4 text-primary bg-gray-200 border-gray-300 rounded"
                    />
                    <div>
                      <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ perm.label }}</span>
                      <p class="text-xs text-gray-500">{{ perm.desc }}</p>
                    </div>
                  </label>
                </div>
              </div>

              <div>
                <label class="block text-sm font-medium text-gray-600 mb-1">有效期 (天，0表示永久)</label>
                <input v-model.number="newKeyExpiresDays" type="number" min="0" max="3650" class="input-field" />
              </div>
            </div>

            <div class="flex gap-3 mt-6">
              <button @click="showGenerateModal = false" class="btn-secondary flex-1">取消</button>
              <button @click="generateApiKey" class="btn-primary flex-1">生成</button>
            </div>
          </div>
        </div>

        <div v-if="generatedKey" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div class="bg-white dark:bg-gray-800 rounded-lg p-6 w-full max-w-md mx-4">
            <h3 class="text-lg font-semibold mb-2">密钥已生成</h3>
            <div class="bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800 rounded-lg p-3 mb-4">
              <p class="text-sm text-yellow-800 dark:text-yellow-200">
                <strong>重要：</strong>密钥只会在此时显示一次，请立即复制保存。
              </p>
            </div>
            <div class="bg-gray-100 dark:bg-gray-900 rounded-lg p-4 mb-4">
              <p class="text-xs text-gray-500 mb-1">你的 API 密钥：</p>
              <code class="text-sm font-mono break-all text-gray-800 dark:text-gray-200">{{ generatedKey.rawKey }}</code>
            </div>
            <button @click="copyToClipboard(generatedKey.rawKey)" class="btn-primary w-full mb-2">复制密钥</button>
            <button @click="generatedKey = null" class="btn-secondary w-full">完成</button>
          </div>
        </div>
      </div>
    </div>

    <WebDAVBrowser :show="showBrowser" :url="settings.webDAVURL" :username="settings.webDAVUsername"
      :password="settings.webDAVPassword" :initialPath="settings.webDAVRemoteDir" @close="showBrowser = false"
      @select="onDirSelect" />

    <div class="card">
      <h3 class="text-lg font-semibold mb-4 text-primary">FFmpeg 环境</h3>

      <!-- 已检测到 ffmpeg -->
      <div v-if="ffmpegStatus?.available" class="space-y-2">
        <div class="flex flex-wrap items-center gap-2">
          <span class="w-2.5 h-2.5 rounded-full bg-green-500"></span>
          <span class="text-sm font-medium text-gray-800 dark:text-gray-100">
            FFmpeg {{ ffmpegStatus.version }} 已就绪
          </span>
          <span class="px-2 py-0.5 text-xs rounded bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300">
            {{ ffmpegSourceLabels[ffmpegStatus.source] || ffmpegStatus.source }}
          </span>
        </div>
        <p class="text-xs text-gray-500 break-all">路径：{{ ffmpegStatus.path }}</p>
        <p class="text-xs text-gray-500">M3U8 分片合并将优先使用 FFmpeg 进行无损转封装。</p>
        <button @click="loadFFmpegStatus(true)" class="btn-secondary text-xs px-3 py-1 mt-1">重新检测</button>
      </div>

      <!-- 未检测到 ffmpeg -->
      <div v-else class="space-y-3">
        <div class="flex items-start gap-2 p-3 rounded-lg bg-yellow-50 border border-yellow-200 dark:bg-yellow-900/20 dark:border-yellow-800">
          <span class="w-2.5 h-2.5 rounded-full bg-yellow-500 mt-1 shrink-0"></span>
          <p class="text-xs text-yellow-800 dark:text-yellow-200">
            未检测到 FFmpeg。M3U8 合并将使用内置 gomedia 封装，对 H.265 及部分特殊编码的兼容性可能不佳。
          </p>
        </div>

        <!-- 安装进行中 -->
        <div v-if="ffmpegStatus?.installing" class="space-y-2">
          <div class="w-full h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
            <div class="h-full bg-primary transition-all duration-300 rounded-full"
              :style="{ width: (ffmpegStatus.progress || 0) + '%' }"></div>
          </div>
          <p class="text-xs text-gray-500">{{ ffmpegStatus.message }}（{{ ffmpegStatus.progress }}%）</p>
        </div>

        <div v-else class="space-y-3">
          <div v-if="ffmpegStatus?.supported" class="flex flex-wrap items-center gap-3">
            <button @click="installFFmpeg" class="btn-primary text-sm">
              私有下载安装（不修改系统环境）
            </button>
            <span class="text-xs text-gray-500">下载到服务器私有目录，仅当前服务使用</span>
          </div>

          <p v-if="ffmpegStatus && !ffmpegStatus.supported" class="text-xs text-red-500">
            当前平台不支持自动私有安装，请参照下方说明手动安装。
          </p>

          <p class="text-xs text-gray-500">
            也可参照
            <a href="https://ffmpeg.org/download.html" target="_blank" rel="noopener noreferrer" style="color:#3498db">FFmpeg 官网</a>
            将 ffmpeg 安装到宿主机 PATH，或通过环境变量 FFMPEG_PATH 指定可执行文件路径。
          </p>
        </div>

        <div v-if="ffmpegStatus?.lastError" class="text-xs text-red-500 whitespace-pre-wrap break-all">
          最近错误：{{ ffmpegStatus.lastError }}
        </div>

        <button @click="loadFFmpegStatus(true)" class="btn-secondary text-xs px-3 py-1">重新检测</button>
      </div>
    </div>

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
