<script setup lang="ts">
import { RouterLink, RouterView, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const authStore = useAuthStore()

const handleLogout = () => {
  authStore.logout()
  router.push('/login')
}
</script>

<template>
  <div class="min-h-screen bg-dark-200">
    <nav v-if="authStore.isAuthenticated" class="bg-dark-100 border-b border-white/5 sticky top-0 z-50">
      <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div class="flex items-center justify-between h-16">
          <div class="flex items-center space-x-8">
            <h1 class="text-2xl font-bold bg-gradient-to-r from-primary to-secondary bg-clip-text text-transparent">
              M3U8 下载器
            </h1>
            <div class="hidden md:flex space-x-4">
              <RouterLink
                to="/"
                class="text-gray-300 hover:text-primary px-3 py-2 rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
              >
                下载
              </RouterLink>
              <RouterLink
                to="/tasks"
                class="text-gray-300 hover:text-primary px-3 py-2 rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
              >
                任务
              </RouterLink>
              <RouterLink
                to="/settings"
                class="text-gray-300 hover:text-primary px-3 py-2 rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
              >
                设置
              </RouterLink>
            </div>
          </div>

          <div v-if="authStore.authEnabled" class="flex items-center">
            <button 
              @click="handleLogout"
              class="text-gray-400 hover:text-red-400 p-2 rounded-lg transition-colors"
              title="退出登录"
            >
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"></path><polyline points="16 17 21 12 16 7"></polyline><line x1="21" y1="12" x2="9" y2="12"></line></svg>
            </button>
          </div>
        </div>
      </div>
    </nav>

    <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
</style>
