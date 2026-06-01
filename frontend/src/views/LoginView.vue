<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const authStore = useAuthStore()

const username = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')

const handleLogin = async () => {
  if (!username.value || !password.value) {
    error.value = '请输入用户名和密码'
    return
  }

  loading.value = true
  error.value = ''

  const res = await authStore.login(username.value, password.value)
  if (res.success) {
    router.push('/')
  } else {
    error.value = res.message || '登录失败'
  }
  loading.value = false
}
</script>

<template>
  <div class="min-h-[80vh] flex items-center justify-center p-4">
    <div class="w-full max-w-md">
      <div class="text-center mb-8">
        <h1 class="text-3xl font-bold text-primary mb-2">M3U8 Downloader</h1>
        <p class="text-gray-400">请登录以继续使用</p>
      </div>

      <div class="card shadow-2xl">
        <form @submit.prevent="handleLogin" class="space-y-6">
          <div>
            <label class="block text-sm font-medium text-gray-300 mb-2">用户名</label>
            <input
              v-model="username"
              type="text"
              class="input-field"
              placeholder="请输入管理员账号"
              autocomplete="username"
              required
            />
          </div>

          <div>
            <label class="block text-sm font-medium text-gray-300 mb-2">密码</label>
            <input
              v-model="password"
              type="password"
              class="input-field"
              placeholder="请输入密码"
              autocomplete="current-password"
              required
            />
          </div>

          <div v-if="error" class="text-red-400 text-sm text-center bg-red-400/10 py-2 rounded-lg border border-red-400/20">
            {{ error }}
          </div>

          <button
            type="submit"
            :disabled="loading"
            class="btn-primary w-full py-3 text-lg font-bold flex items-center justify-center gap-2"
          >
            <svg v-if="loading" class="animate-spin h-5 w-5" viewBox="0 0 24 24">
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" fill="none"></circle>
              <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
            </svg>
            {{ loading ? '登录中...' : '登录' }}
          </button>
        </form>
      </div>

      <div class="mt-8 text-center text-xs text-gray-500">
        <p>提示: 账号密码由服务端环境变量 AUTH_USERNAME 和 AUTH_PASSWORD 控制</p>
      </div>
    </div>
  </div>
</template>
