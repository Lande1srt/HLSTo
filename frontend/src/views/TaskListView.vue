<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted, computed } from 'vue'
import { taskAPI, settingsAPI, saveBlob } from '@/api'
import { buildWsUrl } from '@/api/runtime'
import { useDownloadStore } from '@/stores/download'
import { useSettingsStore } from '@/stores/settings'
import WebDAVBrowser from './WebDAVBrowser.vue'
import type { Task } from '@/stores/download'

const downloadStore = useDownloadStore()
const settingsStore = useSettingsStore()
const tasks = ref<Task[]>([])
type TaskFilter = 'all' | 'pending' | 'downloading' | 'merging' | 'compressing' | 'packing' | 'uploading' | 'completed' | 'failed' | 'paused'
const filter = ref<TaskFilter>('all')
// 状态筛选项（桌面端按钮组、移动端下拉框共用）
const FILTER_OPTIONS: { value: TaskFilter; label: string }[] = [
  { value: 'all', label: '全部' },
  { value: 'pending', label: '等待中' },
  { value: 'downloading', label: '下载中' },
  { value: 'merging', label: '合并中' },
  { value: 'compressing', label: '压缩中' },
  { value: 'packing', label: '分片中' },
  { value: 'uploading', label: '上传中' },
  { value: 'paused', label: '已暂停' },
  { value: 'completed', label: '已完成' },
  { value: 'failed', label: '失败' }
]
// 当前筛选中文标签（自定义下拉触发器显示）
const currentFilterLabel = computed(
  () => FILTER_OPTIONS.find(f => f.value === filter.value)?.label ?? '全部'
)
// 移动端自定义筛选下拉
const filterDropdownOpen = ref(false)
const filterSelectEl = ref<HTMLElement | null>(null)
const selectFilter = (value: TaskFilter) => {
  filter.value = value
  filterDropdownOpen.value = false
}
// 点击组件外部时收起下拉
const onFilterDocClick = (e: MouseEvent) => {
  const el = filterSelectEl.value
  if (el && !el.contains(e.target as Node)) {
    filterDropdownOpen.value = false
  }
}
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

// 高频 progress 消息做"按帧合批"：多个任务、每条消息只写入暂存，
// 在下一个微任务/动画帧统一落盘到响应式数组，避免每秒数十次触发
// filteredTasks 全量 filter+sort 与整页卡片重渲染导致卡顿。
const pendingProgress = new Map<string, Partial<Task>>()
let progressFlushScheduled = false

const scheduleProgressFlush = () => {
  if (progressFlushScheduled) return
  progressFlushScheduled = true
  // queueMicroTask 会在同一秒内仍可能多次；用 rAF 对齐浏览器绘制节奏（约每帧一次）
  const raf = window.requestAnimationFrame || ((cb: FrameRequestCallback) => window.setTimeout(() => cb(performance.now()), 50))
  raf(() => {
    progressFlushScheduled = false
    if (pendingProgress.size === 0) return
    pendingProgress.forEach((patch, id) => {
      const task = taskIndex.value.get(id)
      if (task) Object.assign(task, patch)
    })
    pendingProgress.clear()
  })
}

// taskIndex: id -> 任务对象的索引，WS 命中 O(1)，替代 findIndex 的 O(N) 扫描
const taskIndex = computed(() => {
  const map = new Map<string, Task>()
  for (const t of tasks.value) map.set(t.id, t)
  return map
})

const connectGlobalWebSocket = () => {
  if (ws.value) {
    ws.value.close()
  }

  const wsUrl = buildWsUrl('/ws')

  ws.value = new WebSocket(wsUrl)

  ws.value.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data)

      // status / 未知新任务才需要走索引查找；progress 走合批
      if (data.type === 'progress') {
        const prev = pendingProgress.get(data.taskId) || {}
        pendingProgress.set(data.taskId, {
          ...prev,
          progress: data.progress,
          speed: data.speed,
          downloadedSegments: data.downloadedSegments,
          totalSegments: data.totalSegments
        })
        scheduleProgressFlush()
        return
      }

      const task = taskIndex.value.get(data.taskId)
      if (task) {
        switch (data.type) {
          case 'status':
            // 立即刷出可能积压的同任务进度，保证完成/失败时显示最终态
            pendingProgress.delete(data.taskId)
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
      console.error('Global WebSocket message error:', error)
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

// ============ 轻量全量兜底刷新 ============
// WS 正常时进度已实时；该轮询只解决 WS 丢消息/状态漂移，10 秒一次、
// 仅在存在活跃任务时发起、且手动刷新进行中则跳过，避免任务过多时的无谓请求与重渲染。
const ACTIVE_STATUSES: Task['status'][] = ['pending', 'downloading', 'merging', 'compressing', 'packing', 'uploading', 'paused']
let fullRefreshTimer: number | null = null
let fullRefreshing = false

const silentRefreshTasks = async () => {
  if (fullRefreshing) return
  // 没有活跃任务则不请求
  const hasActive = tasks.value.some(t => ACTIVE_STATUSES.includes(t.status))
  if (!hasActive) return
  fullRefreshing = true
  try {
    const response = await taskAPI.list()
    if (response.data?.code === 200) {
      // 先刷出合批中的进度，避免全量数据覆盖为稍旧的值
      if (pendingProgress.size > 0) {
        pendingProgress.forEach((patch, id) => {
          const task = taskIndex.value.get(id)
          if (task) Object.assign(task, patch)
        })
        pendingProgress.clear()
      }
      mergeTaskList(response.data.data || [])
    }
  } catch (error) {
    console.error('Failed to silent refresh tasks:', error)
  } finally {
    fullRefreshing = false
  }
}

// mergeTaskList 增量合并：保留已有对象引用（WS 已在其上更新），
// 仅同步变化字段，避免整组替换导致整页 v-for DOM 重建。
const mergeTaskList = (list: Task[]) => {
  const incomingIds = new Set<string>()
  for (const incoming of list) {
    incomingIds.add(incoming.id)
    const existing = taskIndex.value.get(incoming.id)
    if (existing) {
      // 进度字段优先信任 WS 合批结果，避免全量响应时序更旧导致回跳
      existing.status = incoming.status
      existing.error = incoming.error ?? ''
      existing.outputPath = incoming.outputPath
      existing.name = incoming.name
      existing.url = incoming.url
      existing.totalSegments = incoming.totalSegments
      existing.totalBytes = incoming.totalBytes
      existing.downloadedBytes = incoming.downloadedBytes
      existing.createdAt = incoming.createdAt
      existing.keyPath = incoming.keyPath
      existing.webDAVURL = incoming.webDAVURL
      // 进度/速度/已下载：全量值更新（WS 间隙的最终一致）
      existing.progress = incoming.progress
      existing.speed = incoming.speed
      existing.downloadedSegments = incoming.downloadedSegments
    } else {
      tasks.value.push(incoming)
    }
  }
  // 移除已被后端删除的任务
  if (tasks.value.length !== list.length) {
    tasks.value = tasks.value.filter(t => incomingIds.has(t.id))
  }
  // 状态可能在刷新间流转（如 downloading -> completed/paused），同步移动端自动展开
  syncAutoExpand(tasks.value)
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
      // 整体替换后旧对象引用失效，强制让过滤/排序缓存重建
      filteredCache = null
      tasks.value = data.data || []
      // 移动端自动展开未完成（非暂停/失败/完成）任务
      syncAutoExpand(tasks.value)
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

// copiedLinkId 记录刚复制的任务，用于短暂的"已复制"反馈
const copiedLinkId = ref('')
let copiedTimer: number | null = null

// copyLink 复制任务下载链接；优先 Clipboard API，非安全上下文(如 Wails 自定义协议)降级 execCommand
const copyLink = async (text: string, id: string) => {
  if (!text) return
  let ok = false
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      ok = true
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.focus()
      ta.select()
      ok = document.execCommand('copy')
      document.body.removeChild(ta)
    }
  } catch (err) {
    console.error('复制链接失败:', err)
    ok = false
  }
  if (ok) {
    copiedLinkId.value = id
    if (copiedTimer !== null) window.clearTimeout(copiedTimer)
    copiedTimer = window.setTimeout(() => {
      copiedLinkId.value = ''
      copiedTimer = null
    }, 1500)
  } else {
    alert('复制失败，请长按链接手动复制')
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

// filteredTasks 结果做指纹缓存：排序/筛选只依赖 status、createdAt、
// 名称/URL/输出路径与筛选条件，与高频变化的 progress/speed 无关。
// progress 合批更新时虽然触发依赖收集，但指纹未变，直接复用上次结果，
// 避免任务量大时每秒多次全量 filter+sort（含 new Date 解析）。
let filteredCache: {
  fingerprint: string
  result: Task[]
} | null = null

const filteredTasks = computed<Task[]>(() => {
  const keyword = searchKeyword.value.toLowerCase().trim()
  const sortOrder = settingsStore.settings.taskSortOrder

  // 指纹：数组长度 + 每条影响排序/筛选的字段（不含 progress 等高频字段）
  const parts: string[] = [
    filter.value,
    keyword,
    sortOrder,
    String(tasks.value.length)
  ]
  for (const t of tasks.value) {
    parts.push(`${t.id}|${t.status}|${t.createdAt}|${t.name}|${t.url}|${t.outputPath ?? ''}`)
  }
  const fingerprint = parts.join('\n')

  if (filteredCache && filteredCache.fingerprint === fingerprint) {
    return filteredCache.result
  }

  let result = tasks.value
  if (filter.value !== 'all') {
    result = result.filter((t: { status: string }) => t.status === filter.value)
  }

  // 搜索过滤
  if (keyword) {
    result = result.filter((t: Task) => {
      const name = t.name?.toLowerCase() || ''
      const url = t.url?.toLowerCase() || ''
      const outputPath = t.outputPath?.toLowerCase() || ''
      return name.includes(keyword) || url.includes(keyword) || outputPath.includes(keyword)
    })
  }

  const sorted = [...result].sort((a, b) => {
    const timeA = new Date(a.createdAt).getTime()
    const timeB = new Date(b.createdAt).getTime()

    if (timeA !== timeB) {
      return sortOrder === 'desc' ? timeB - timeA : timeA - timeB
    }

    return a.id.localeCompare(b.id)
  })

  filteredCache = { fingerprint, result: sorted }
  return sorted
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

// ============ 卡片折叠（移动端优先）============
// 默认收起，仅显示标题与状态；展开后才显示链接、操作按钮、进度等。
const expandedIds = ref<Set<string>>(new Set())
const toggleExpand = (id: string) => {
  const next = new Set(expandedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedIds.value = next
}
const isExpanded = (id: string) => expandedIds.value.has(id)

// ============ 移动端自动展开未完成任务，便于查看进度 ============
// 仅窄屏(<=640px)生效；completed/paused/failed 不自动展开。
// autoExpandedIds 记录"由系统自动展开"的卡片，任务结束时只收起这些；
// userCollapsedAuto 记录用户手动折叠的活跃任务，后续刷新不再强制展开。
const MOBILE_AUTO_EXPAND_MAX = 640
const isMobileView = () =>
  typeof window !== 'undefined' &&
  window.matchMedia(`(max-width: ${MOBILE_AUTO_EXPAND_MAX}px)`).matches
// 需自动展开的状态：等待队列 + 五个执行阶段；排除 paused/completed/failed
const AUTO_EXPAND_STATUSES: Task['status'][] = ['pending', 'downloading', 'merging', 'compressing', 'packing', 'uploading']
const autoExpandedIds = new Set<string>()
const userCollapsedAuto = new Set<string>()

// 依据当前任务状态同步展开集合（移动端）
const syncAutoExpand = (list: Task[]) => {
  if (!isMobileView()) return
  const next = new Set(expandedIds.value)
  for (const t of list) {
    const shouldOpen = AUTO_EXPAND_STATUSES.includes(t.status)
    if (shouldOpen) {
      // 用户手动折叠过的活跃任务，尊重其选择不自动打开
      if (!userCollapsedAuto.has(t.id) && !next.has(t.id)) {
        next.add(t.id)
        autoExpandedIds.add(t.id)
      }
    } else if (autoExpandedIds.has(t.id)) {
      // 仅收起"由自动展开打开"的卡片；用户手动展开的保持不动
      next.delete(t.id)
      autoExpandedIds.delete(t.id)
      userCollapsedAuto.delete(t.id)
    }
  }
  expandedIds.value = next
}

// 用户手动切换展开状态时，维护自动/手动标记，避免与自动逻辑冲突
const onManualToggle = (task: Task) => {
  const wasOpen = expandedIds.value.has(task.id)
  if (!wasOpen) {
    // 手动展开：清除折叠标记，并视为非自动展开
    userCollapsedAuto.delete(task.id)
    autoExpandedIds.delete(task.id)
  } else {
    // 手动折叠
    autoExpandedIds.delete(task.id)
    if (isMobileView() && AUTO_EXPAND_STATUSES.includes(task.status)) {
      userCollapsedAuto.add(task.id)
    }
  }
  toggleExpand(task.id)
}

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

// WS 实时状态流转（downloading->completed/paused/failed）时同步移动端自动展开。
// 仅监听 status 指纹，避免 progress/speed 高频更新反复触发。
const statusFingerprint = computed(() =>
  tasks.value.map(t => `${t.id}:${t.status}`).join('|')
)
watch(statusFingerprint, () => {
  syncAutoExpand(tasks.value)
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

// isM3U8Task 判定是否为 HLS(m3u8) 任务：m3u8 用片段数，普通视频用字节数
const isM3U8Task = (task: Task) => !!task.url?.toLowerCase().includes('.m3u8')

// getProgressText 统一各状态下的进度主文本：
//  - 仅在下载/合并阶段的 m3u8 任务显示「已下载片段 / 总片段 片段」；
//  - 其余阶段（压缩/分片/上传等）后端复用 segments 字段传的是字节 KB，
//    统一按容量显示，避免上传阶段把字节数误显示成"片段"。
//  - REST 全量使用 bytes(字节)；实时 WS 把 KB 复用在 segments 字段。
const SEGMENT_STATUS: Task['status'][] = ['downloading', 'merging']
const getProgressText = (task: Task): string => {
  if (isM3U8Task(task) && SEGMENT_STATUS.includes(task.status)) {
    return `${task.downloadedSegments} / ${task.totalSegments} 片段`
  }
  const totalKB = task.totalBytes
    ? task.totalBytes / 1024
    : task.totalSegments
  const doneKB = task.downloadedBytes
    ? task.downloadedBytes / 1024
    : task.downloadedSegments
  return `${formatSize(doneKB)} / ${formatSize(totalKB)}`
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

  if (isM3U8Task(task) && SEGMENT_STATUS.includes(status)) {
    // m3u8 下载/合并：片段数，按每片约 512KB 估算
    downloadedKB = task.downloadedSegments * 512
    totalKB = task.totalSegments * 512
  } else {
    // 普通视频或上传等阶段：优先用持久化字节数(/1024 得 KB)，否则回退 WS 复用的 segments(KB)
    totalKB = task.totalBytes ? task.totalBytes / 1024 : task.totalSegments
    downloadedKB = task.downloadedBytes ? task.downloadedBytes / 1024 : task.downloadedSegments
  }
  
  if (totalKB <= 0 || downloadedKB >= totalKB) return ''
  
  const remainingKB = totalKB - downloadedKB
  const remainingSeconds = remainingKB / speedKBps
  
  if (remainingSeconds <= 0) return ''
  
  return formatTime(remainingSeconds)
}

// 跨断点（桌面<->移动、屏幕旋转）时重新同步自动展开
let mobileMql: MediaQueryList | null = null
const onMobileBreakpointChange = () => {
  syncAutoExpand(tasks.value)
}

onMounted(() => {
  settingsStore.loadSettings()
  loadTasks()
  connectGlobalWebSocket()
  // 10 秒轻量兜底，仅在有活跃任务时真正发请求
  fullRefreshTimer = window.setInterval(silentRefreshTasks, 10000)
  if (typeof window !== 'undefined' && window.matchMedia) {
    mobileMql = window.matchMedia(`(max-width: ${MOBILE_AUTO_EXPAND_MAX}px)`)
    mobileMql.addEventListener('change', onMobileBreakpointChange)
  }
  // 自定义筛选下拉：点击外部收起
  document.addEventListener('click', onFilterDocClick)
})

onUnmounted(() => {
  if (ws.value) {
    ws.value.close()
    ws.value = null
  }
  if (fullRefreshTimer !== null) {
    window.clearInterval(fullRefreshTimer)
    fullRefreshTimer = null
  }
  if (mobileMql) {
    mobileMql.removeEventListener('change', onMobileBreakpointChange)
    mobileMql = null
  }
  document.removeEventListener('click', onFilterDocClick)
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

      <!-- 桌面端：状态筛选按钮组（可横向滚动） -->
      <div class="filter-tabs flex gap-2 mb-4 overflow-x-auto pb-2">
        <button
          v-for="f in FILTER_OPTIONS"
          :key="f.value"
          @click="filter = f.value"
          class="px-4 py-2 rounded-lg text-sm font-medium transition-colors whitespace-nowrap"
          :class="filter === f.value
            ? 'bg-primary text-white'
            : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
        >
          {{ f.label }}
        </button>
      </div>

      <!-- 移动端：状态筛选改为自定义下拉框，避免一排按钮左右滑动 -->
      <div class="filter-select-wrap mb-4">
        <div class="custom-select" ref="filterSelectEl">
          <button
            type="button"
            class="custom-select-trigger"
            :class="{ 'is-open': filterDropdownOpen }"
            aria-haspopup="listbox"
            :aria-expanded="filterDropdownOpen"
            @click="filterDropdownOpen = !filterDropdownOpen"
          >
            <span class="custom-select-value">{{ currentFilterLabel }}</span>
            <svg
              class="custom-select-chevron"
              :class="{ 'is-open': filterDropdownOpen }"
              xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
            >
              <polyline points="6 9 12 15 18 9" />
            </svg>
          </button>
          <transition name="dropdown">
            <ul v-if="filterDropdownOpen" class="custom-select-menu" role="listbox">
              <li
                v-for="f in FILTER_OPTIONS"
                :key="f.value"
                role="option"
                :aria-selected="filter === f.value"
                class="custom-select-option"
                :class="{ 'is-selected': filter === f.value }"
                @click="selectFilter(f.value)"
              >
                <span>{{ f.label }}</span>
                <svg v-if="filter === f.value" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
              </li>
            </ul>
          </transition>
        </div>
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
          class="task-card card hover:shadow-md transition-all"
        >
          <!-- 折叠头：标题 + 状态 + 展开箭头，单行不换行、超出省略 -->
          <div class="task-card-header" @click="onManualToggle(task)">
            <div class="task-card-titlewrap">
              <h3 class="task-card-title" :title="`${task.name}.mp4`">
                {{ task.name }}.mp4
              </h3>
            </div>
            <span
              class="task-card-status px-3 py-1 rounded-full text-xs font-medium shrink-0"
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
            <svg
              class="task-card-chevron shrink-0 transition-transform"
              :class="{ 'is-open': isExpanded(task.id) }"
              xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
            >
              <polyline points="6 9 12 15 18 9" />
            </svg>
          </div>

          <!-- 展开体：链接、操作按钮、进度、时间等 -->
          <div v-show="isExpanded(task.id)" class="task-card-body">
            <!-- 链接独占一行：链接省略 + 复制按钮（仅移动端显示） -->
            <div class="task-card-urlrow mb-2">
              <p class="task-card-url text-sm text-gray-500" :title="task.url">
                {{ task.url }}
              </p>
              <button
                type="button"
                class="copy-link-btn"
                :class="{ 'is-copied': copiedLinkId === task.id }"
                @click.stop="copyLink(task.url, task.id)"
                :title="copiedLinkId === task.id ? '已复制' : '复制链接'"
              >
                <svg v-if="copiedLinkId !== task.id" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>
                <svg v-else xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
                <span class="copy-link-text">{{ copiedLinkId === task.id ? '已复制' : '复制' }}</span>
              </button>
            </div>
            <!-- 操作按钮独立成行，整体右对齐 -->
            <div class="flex items-center justify-end gap-2 mb-3 action-buttons">
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

          <div v-if="isTaskRunning(task) || task.status === 'paused'" class="mb-3">
            <div class="flex justify-between text-xs text-gray-500 mb-1">
                <span>{{ getProgressText(task) }}</span>
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
/* ===== 状态筛选：桌面按钮组 / 移动端自定义下拉框 ===== */
/* 默认（桌面）：显示按钮组，隐藏下拉框 */
.filter-select-wrap {
  display: none;
}
/* 窄屏：隐藏横向按钮组，改为整行自定义下拉，杜绝左右滑动 */
@media (max-width: 640px) {
  .filter-tabs {
    display: none;
  }
  .filter-select-wrap {
    display: block;
  }
}

/* 自定义下拉触发器 */
.custom-select {
  position: relative;
  width: 100%;
}
.custom-select-trigger {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 8px 12px;
  border: 1px solid #d1d5db;
  border-radius: 8px;
  background: #fff;
  color: #374151;
  font-size: 14px;
  line-height: 1.25;
  cursor: pointer;
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
}
.custom-select-trigger.is-open {
  border-color: var(--color-primary, #4f46e5);
  box-shadow: 0 0 0 2px rgba(79, 70, 229, 0.15);
}
.custom-select-value {
  flex: 1 1 auto;
  min-width: 0;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.custom-select-chevron {
  flex: 0 0 auto;
  margin-left: 8px;
  color: #9ca3af;
  transition: transform 0.2s ease;
}
.custom-select-chevron.is-open {
  transform: rotate(180deg);
}

/* 下拉菜单：相对触发器定位，带阴影与最大高度滚动 */
.custom-select-menu {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  z-index: 30;
  margin: 0;
  padding: 4px;
  list-style: none;
  max-height: 264px;
  overflow-y: auto;
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
}
.custom-select-option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 9px 10px;
  border-radius: 6px;
  font-size: 14px;
  color: #374151;
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease;
}
.custom-select-option:hover,
.custom-select-option:active {
  background: #f3f4f6;
}
.custom-select-option.is-selected {
  color: var(--color-primary, #4f46e5);
  font-weight: 600;
  background: rgba(79, 70, 229, 0.08);
}

/* 展开/收起过渡 */
.dropdown-enter-active,
.dropdown-leave-active {
  transition: opacity 0.15s ease, transform 0.15s ease;
}
.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}

/* 深色模式 */
.dark .custom-select-trigger {
  background: #1f2937;
  border-color: #4b5563;
  color: #e5e7eb;
}
.dark .custom-select-menu {
  background: #1f2937;
  border-color: #374151;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
}
.dark .custom-select-option {
  color: #e5e7eb;
}
.dark .custom-select-option:hover,
.dark .custom-select-option:active {
  background: #374151;
}
.dark .custom-select-option.is-selected {
  background: rgba(99, 102, 241, 0.18);
}

/* ===== 任务卡片折叠（移动端优先）===== */
.task-card {
  padding: 0;
  overflow: hidden;
}

/* 折叠头：一行内 标题(可伸缩省略) + 状态 + 箭头，点击整行展开 */
.task-card-header {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 48px;
  padding: 8px 12px;
  cursor: pointer;
  user-select: none;
}

.task-card-titlewrap {
  flex: 1 1 auto;
  min-width: 0;
}

/* 标题强制单行，超出省略，绝不换行 */
.task-card-title {
  margin: 0;
  font-weight: 600;
  font-size: 14px;
  color: #1f2937;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.dark .task-card-title {
  color: #f3f4f6;
}

.task-card-chevron {
  color: #9ca3af;
  transition: transform 0.2s ease;
}
.task-card-chevron.is-open {
  transform: rotate(180deg);
}

/* 展开体恢复卡片内边距 */
.task-card-body {
  padding: 4px 12px 12px;
  border-top: 1px solid rgba(0, 0, 0, 0.06);
}
.dark .task-card-body {
  border-top-color: rgba(255, 255, 255, 0.08);
}

/* 链接行：链接(flex 省略) + 复制按钮，垂直居中对齐 */
.task-card-urlrow {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

/* 链接单行省略，不换行，占满剩余空间 */
.task-card-url {
  flex: 1 1 auto;
  min-width: 0;
  margin: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 复制链接按钮：桌面端与移动端均显示（图标+文字） */
.copy-link-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex: 0 0 auto;
  padding: 4px 8px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  background: #fff;
  color: #6b7280;
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
  transition: color 0.15s ease, border-color 0.15s ease, background 0.15s ease;
}
.copy-link-btn:hover {
  color: var(--color-primary, #4f46e5);
  border-color: var(--color-primary, #4f46e5);
}
.copy-link-btn.is-copied {
  color: #16a34a;
  border-color: #16a34a;
}
.dark .copy-link-btn {
  background: #1f2937;
  border-color: #4b5563;
  color: #9ca3af;
}

/* 窄屏：操作按钮组允许换行，按钮保持可点尺寸 */
@media (max-width: 640px) {
  .task-card-body .action-buttons {
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 2px;
  }
}
</style>
