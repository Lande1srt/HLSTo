<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { useSettingsStore } from '@/stores/settings'

const settingsStore = useSettingsStore()

interface DiskInfo {
  mountPoint: string
  device: string
  total: number
  used: number
  free: number
  freePercent: number
  usedPercent: number
  colorClass: string
  fsType: string
}

const allDisks = ref<DiskInfo[]>([])
const currentDisk = ref<{ total: number; used: number; free: number; freePercent: number } | null>(null)
const loadingDisks = ref(false)
let refreshTimer: ReturnType<typeof setInterval> | null = null

const loadCurrentDisk = async () => {
  try {
    const response = await fetch('/api/disk/info')
    const data = await response.json()
    if (data.code === 200 && data.data) {
      currentDisk.value = data.data
    }
  } catch (error) {
    console.error('Failed to load current disk info:', error)
  }
}

const loadAllDisks = async () => {
  loadingDisks.value = true
  try {
    const response = await fetch('/api/disk/all')
    const data = await response.json()
    if (data.code === 200 && data.data) {
      setTimeout(() => {
        allDisks.value = data.data
      }, 50)
    }
  } catch (error) {
    console.error('Failed to load all disks:', error)
  } finally {
    loadingDisks.value = false
  }
}

const formatSize = (bytes: number): string => {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

const getColorClass = (colorClass: string): string => {
  switch (colorClass) {
    case 'danger': return 'bg-red-500'
    case 'warning': return 'bg-yellow-500'
    case 'info': return 'bg-blue-500'
    case 'success':
    default: return 'bg-green-500'
  }
}

const getStatusText = (colorClass: string): string => {
  switch (colorClass) {
    case 'danger': return '空间紧张'
    case 'warning': return '使用较高'
    case 'info': return '正常使用'
    case 'success':
    default: return '空间充足'
  }
}

const getStatusBadgeClass = (colorClass: string): string => {
  switch (colorClass) {
    case 'danger': return 'bg-red-100 text-red-700'
    case 'warning': return 'bg-yellow-100 text-yellow-700'
    case 'info': return 'bg-blue-100 text-blue-700'
    case 'success':
    default: return 'bg-green-100 text-green-700'
  }
}

const diskStatusColor = ref('bg-gray-500')

const updateDiskStatusColor = () => {
  if (!currentDisk.value) {
    diskStatusColor.value = 'bg-gray-500'
    return
  }
  if (currentDisk.value.freePercent < 10) {
    diskStatusColor.value = 'bg-red-500'
  } else if (currentDisk.value.freePercent < 20) {
    diskStatusColor.value = 'bg-yellow-500'
  } else {
    diskStatusColor.value = 'bg-green-500'
  }
}

const startRefreshTimer = () => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
  }
  const interval = settingsStore.settings.diskRefreshInterval * 1000
  if (interval > 0) {
    refreshTimer = setInterval(async () => {
      await loadCurrentDisk()
      await loadAllDisks()
      updateDiskStatusColor()
    }, interval)
  }
}

const stopRefreshTimer = () => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
}

watch(() => settingsStore.settings.diskRefreshInterval, () => {
  startRefreshTimer()
})

onMounted(async () => {
  await settingsStore.loadSettings()
  await loadCurrentDisk()
  await loadAllDisks()
  updateDiskStatusColor()
  startRefreshTimer()
})

onUnmounted(() => {
  stopRefreshTimer()
})
</script>

<template>
  <div class="max-w-4xl mx-auto">
    <div class="mb-6">
      <h1 class="text-2xl font-bold text-gray-800 dark:text-gray-100 mb-2">磁盘监控</h1>
      <p class="text-gray-500 dark:text-gray-400">实时查看系统磁盘空间使用情况</p>
    </div>

    <div class="bg-white dark:bg-dark-300 rounded-xl shadow-sm border border-gray-200 dark:border-dark-400 p-6 mb-6">
      <h2 class="text-lg font-semibold text-gray-800 dark:text-gray-100 mb-4">当前工作磁盘</h2>
      
      <div v-if="currentDisk" class="space-y-4">
        <div class="grid grid-cols-3 gap-4">
          <div class="bg-gray-50 dark:bg-dark-200 rounded-lg p-4 text-center">
            <div class="text-2xl font-bold text-gray-800 dark:text-gray-100">{{ formatSize(currentDisk.total) }}</div>
            <div class="text-sm text-gray-500 dark:text-gray-400 mt-1">总容量</div>
          </div>
          <div class="bg-gray-50 dark:bg-dark-200 rounded-lg p-4 text-center">
            <div class="text-2xl font-bold text-gray-600 dark:text-gray-300">{{ formatSize(currentDisk.used) }}</div>
            <div class="text-sm text-gray-500 dark:text-gray-400 mt-1">已使用</div>
          </div>
          <div class="bg-gray-50 dark:bg-dark-200 rounded-lg p-4 text-center">
            <div class="text-2xl font-bold" :class="currentDisk.freePercent < 10 ? 'text-red-500' : currentDisk.freePercent < 20 ? 'text-yellow-500' : 'text-green-500'">
              {{ formatSize(currentDisk.free) }}
            </div>
            <div class="text-sm text-gray-500 dark:text-gray-400 mt-1">可用空间</div>
          </div>
        </div>
        
        <div>
          <div class="flex items-center justify-between mb-2">
            <span class="text-sm text-gray-600 dark:text-gray-400">使用进度</span>
            <span class="text-sm font-medium text-gray-800 dark:text-gray-100">
              {{ (100 - currentDisk.freePercent).toFixed(1) }}%
            </span>
          </div>
          <div class="w-full bg-gray-200 dark:bg-dark-400 rounded-full h-3">
            <div 
              :class="diskStatusColor" 
              class="h-3 rounded-full transition-all duration-500" 
              :style="{ width: `${100 - currentDisk.freePercent}%` }"
            ></div>
          </div>
        </div>
      </div>
      
      <div v-else class="text-center py-8 text-gray-500 dark:text-gray-400">
        <svg class="animate-spin h-8 w-8 mx-auto mb-3" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
          <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
          <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
        </svg>
        <p>加载磁盘信息中...</p>
      </div>
    </div>

    <div class="bg-white dark:bg-dark-300 rounded-xl shadow-sm border border-gray-200 dark:border-dark-400 p-6">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-lg font-semibold text-gray-800 dark:text-gray-100">所有磁盘分区</h2>
        <button 
          @click="loadAllDisks" 
          :disabled="loadingDisks"
          class="flex items-center gap-2 px-4 py-2 bg-primary hover:bg-primary-dark text-white text-sm rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          <svg v-if="loadingDisks" class="animate-spin h-4 w-4" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
            <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
            <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
          </svg>
          <svg v-else xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <polyline points="23 4 23 10 17 10"></polyline>
            <polyline points="1 20 1 14 7 14"></polyline>
            <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
          </svg>
          {{ loadingDisks ? '刷新中...' : '刷新' }}
        </button>
      </div>
      
      <div class="relative min-h-[200px]">
        <!-- 加载遮罩层 -->
        <div 
          v-if="loadingDisks && allDisks.length > 0" 
          class="absolute inset-0 bg-white/50 dark:bg-dark-300/50 backdrop-blur-sm rounded-lg flex items-center justify-center z-10"
        >
          <div class="flex items-center gap-2 text-gray-500 dark:text-gray-400">
            <svg class="animate-spin h-5 w-5" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
              <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
            </svg>
            <span class="text-sm">刷新中...</span>
          </div>
        </div>
        
        <!-- 磁盘列表 -->
        <div v-if="allDisks.length > 0" class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          <div 
            v-for="disk in allDisks" 
            :key="disk.mountPoint"
            class="bg-gray-50 dark:bg-dark-200 rounded-lg p-4 border border-gray-200 dark:border-dark-400 hover:border-gray-300 dark:hover:border-dark-300 transition-all duration-300"
          >
            <div class="flex items-center justify-between mb-2">
              <span class="text-sm font-medium text-gray-800 dark:text-gray-100">{{ disk.device }}</span>
              <span :class="getStatusBadgeClass(disk.colorClass)" class="text-xs px-2 py-0.5 rounded-full">
                {{ getStatusText(disk.colorClass) }}
              </span>
            </div>
            <div class="text-xs text-gray-500 dark:text-gray-400 mb-2 truncate" :title="disk.mountPoint">
              <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"></path>
                <circle cx="9" cy="7" r="4"></circle>
                <path d="M22 21v-2a4 4 0 0 0-3-3.87"></path>
                <path d="M16 3.13a4 4 0 0 1 0 7.75"></path>
              </svg>
              {{ disk.mountPoint }}
            </div>
            <div class="text-xs text-gray-500 dark:text-gray-400 mb-3">
              <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path>
                <polyline points="14 2 14 8 20 8"></polyline>
                <line x1="16" y1="13" x2="8" y2="13"></line>
                <line x1="16" y1="17" x2="8" y2="17"></line>
                <polyline points="10 9 9 9 8 9"></polyline>
              </svg>
              文件系统: {{ disk.fsType }}
            </div>
            <div class="flex justify-between text-sm text-gray-600 dark:text-gray-300 mb-2">
              <span>已用: {{ formatSize(disk.used) }}</span>
              <span>可用: {{ formatSize(disk.free) }}</span>
            </div>
            <div class="w-full bg-gray-200 dark:bg-dark-400 rounded-full h-2">
              <div 
                :class="getColorClass(disk.colorClass)" 
                class="h-2 rounded-full transition-all duration-500" 
                :style="{ width: `${disk.usedPercent}%` }"
              ></div>
            </div>
            <div class="flex justify-between text-xs text-gray-500 dark:text-gray-400 mt-2">
              <span>总容量: {{ formatSize(disk.total) }}</span>
              <span class="font-medium">{{ disk.usedPercent.toFixed(1) }}%</span>
            </div>
          </div>
        </div>
        
        <!-- 初始加载状态 -->
        <div v-else-if="loadingDisks && allDisks.length === 0" class="text-center py-8 text-gray-500 dark:text-gray-400">
          <svg class="animate-spin h-8 w-8 mx-auto mb-3" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
            <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
            <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
          </svg>
          <p>加载磁盘列表中...</p>
        </div>
        
        <!-- 空状态 -->
        <div v-else class="text-center py-8 text-gray-500 dark:text-gray-400">
          <svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
            <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path>
            <polyline points="14 2 14 8 20 8"></polyline>
            <line x1="16" y1="13" x2="8" y2="13"></line>
            <line x1="16" y1="17" x2="8" y2="17"></line>
            <polyline points="10 9 9 9 8 9"></polyline>
          </svg>
          <p class="mt-3">未检测到磁盘分区信息</p>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
</style>