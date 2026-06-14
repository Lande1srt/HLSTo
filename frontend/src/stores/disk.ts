import { defineStore } from 'pinia'
import { ref } from 'vue'
import { diskAPI } from '@/api'

export interface DiskInfo {
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

export const useDiskStore = defineStore('disk', () => {
  const diskInfo = ref<DiskInfo | null>(null)
  const allDisks = ref<DiskInfo[]>([])
  const checkingSpace = ref(false)

  const getDiskInfo = async () => {
    try {
      const response = await diskAPI.getInfo()
      if (response.data.code === 200 && response.data.data) {
        diskInfo.value = response.data.data
      }
    } catch (error) {
      console.error('Failed to get disk info:', error)
    }
  }

  const getAllDisks = async () => {
    try {
      const response = await diskAPI.getAllDisks()
      if (response.data.code === 200 && response.data.data) {
        allDisks.value = response.data.data
      }
    } catch (error) {
      console.error('Failed to get all disks:', error)
    }
  }

  const checkSpace = async (path?: string, requiredMB?: number): Promise<{ enough: boolean; freeMB: number; requiredMB?: number }> => {
    checkingSpace.value = true
    try {
      const response = await diskAPI.checkSpace(path)
      if (response.data.code === 200 && response.data.data) {
        const freeMB = response.data.data.free / (1024 * 1024)
        if (requiredMB !== undefined) {
          return {
            enough: freeMB >= requiredMB,
            freeMB: Math.floor(freeMB),
            requiredMB
          }
        }
        return {
          enough: true,
          freeMB: Math.floor(freeMB)
        }
      }
      return { enough: true, freeMB: 0 }
    } catch (error) {
      console.error('Failed to check disk space:', error)
      return { enough: true, freeMB: 0 }
    } finally {
      checkingSpace.value = false
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
      case 'danger':
        return 'bg-red-500'
      case 'warning':
        return 'bg-yellow-500'
      case 'info':
        return 'bg-blue-500'
      case 'success':
      default:
        return 'bg-green-500'
    }
  }

  const getStatusText = (colorClass: string): string => {
    switch (colorClass) {
      case 'danger':
        return '空间紧张'
      case 'warning':
        return '使用较高'
      case 'info':
        return '正常使用'
      case 'success':
      default:
        return '空间充足'
    }
  }

  return {
    diskInfo,
    allDisks,
    checkingSpace,
    getDiskInfo,
    getAllDisks,
    checkSpace,
    formatSize,
    getColorClass,
    getStatusText
  }
})