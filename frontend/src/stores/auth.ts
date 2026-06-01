import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import api from '@/api'

export const useAuthStore = defineStore('auth', () => {
  const token = ref(localStorage.getItem('token') || '')
  const authEnabled = ref(true) // 默认开启，等待 checkAuth 确认

  const isAuthenticated = computed(() => {
    if (!authEnabled.value) return true
    return !!token.value
  })

  const setToken = (newToken: string) => {
    token.value = newToken
    if (newToken) {
      localStorage.setItem('token', newToken)
    } else {
      localStorage.removeItem('token')
    }
  }

  const checkAuth = async () => {
    try {
      const res = await api.get('/auth/check')
      if (res.data.code === 200) {
        authEnabled.value = res.data.data.authEnabled
        if (!res.data.data.authenticated) {
          setToken('')
        }
      }
    } catch (err) {
      console.error('Check auth failed:', err)
    }
  }

  const login = async (username: string, password: string) => {
    try {
      const res = await api.post('/auth/login', { username, password })
      if (res.data.code === 200) {
        setToken(res.data.data.token)
        return { success: true }
      }
      return { success: false, message: res.data.message || '登录失败' }
    } catch (err: unknown) {
      const error = err as { response?: { data?: { message?: string } } }
      return { 
        success: false, 
        message: error.response?.data?.message || '网络错误，请稍后再试' 
      }
    }
  }

  const logout = () => {
    setToken('')
  }

  return {
    token,
    authEnabled,
    isAuthenticated,
    setToken,
    checkAuth,
    login,
    logout
  }
})
