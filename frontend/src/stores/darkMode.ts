import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export const useDarkModeStore = defineStore('darkMode', () => {
  const isDark = ref(false)

  const loadDarkMode = () => {
    const saved = localStorage.getItem('darkMode')
    if (saved !== null) {
      isDark.value = saved === 'true'
      if (isDark.value) {
        document.documentElement.classList.add('dark')
      }
    }
  }

  const toggleDarkMode = () => {
    isDark.value = !isDark.value
  }

  const setDarkMode = (value: boolean) => {
    isDark.value = value
  }

  watch(isDark, (newVal) => {
    localStorage.setItem('darkMode', String(newVal))
    if (newVal) {
      document.documentElement.classList.add('dark')
    } else {
      document.documentElement.classList.remove('dark')
    }
  })

  return {
    isDark,
    loadDarkMode,
    toggleDarkMode,
    setDarkMode
  }
})
