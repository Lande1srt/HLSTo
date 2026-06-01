import { defineStore } from 'pinia'
import { ref } from 'vue'
import { settingsAPI } from '@/api'

export interface Settings {
  defaultThreadCount: number
  defaultOutputName: string
  defaultSavePath: string
  autoClear: boolean
  hostType: string
  enableWebDAV: boolean
  webDAVURL: string
  webDAVUsername: string
  webDAVPassword: string
  webDAVRemoteDir: string
  deleteAfterUpload: boolean
}

export const useSettingsStore = defineStore('settings', () => {
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
    deleteAfterUpload: false
  })

  const loadSettings = async () => {
    try {
      const response = await settingsAPI.get()
      const data = response.data
      if (data.code === 200 && data.data) {
        settings.value = { ...settings.value, ...data.data }
      }
    } catch (error) {
      console.error('Failed to load settings:', error)
    }
  }

  const saveSettings = async (newSettings: Settings) => {
    try {
      const response = await settingsAPI.save(newSettings)
      const data = response.data
      if (data.code === 200) {
        settings.value = newSettings
        return true
      }
      return false
    } catch (error) {
      console.error('Failed to save settings:', error)
      return false
    }
  }

  return {
    settings,
    loadSettings,
    saveSettings
  }
})
