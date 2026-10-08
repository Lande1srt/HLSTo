<script setup lang="ts">
import { RouterLink, RouterView, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useDarkModeStore } from '@/stores/darkMode'
import { onMounted, watch } from 'vue'

const router = useRouter()
const authStore = useAuthStore()
const darkModeStore = useDarkModeStore()

const handleLogout = () => {
  authStore.logout()
  router.push('/login')
}

const checkAndRedirect = async () => {
  await authStore.checkAuth()
  
  const currentRoute = router.currentRoute.value
  
  if (authStore.authEnabled && !authStore.isAuthenticated && currentRoute.name !== 'login') {
    router.push('/login')
  } else if (authStore.isAuthenticated && currentRoute.name === 'login') {
    router.push('/')
  }
}

watch(() => router.currentRoute.value, async () => {
  await checkAndRedirect()
}, { immediate: true })

onMounted(async () => {
  darkModeStore.loadDarkMode()
  await checkAndRedirect()
})
</script>

<template>
  <div class="min-h-screen bg-[#ecf0f1] dark:bg-dark-200">
    <nav v-if="authStore.isAuthenticated" class="bg-white dark:bg-dark-200 border-b border-gray-300 dark:border-dark-400 sticky top-0 z-50">
      <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div class="flex items-center justify-between h-16">
          <div class="flex items-center space-x-8">
            <h1 class="text-2xl font-bold text-[#2c3e50] dark:text-gray-100">
              HLSTo
            </h1>
            <div class="hidden md:flex space-x-4">
              <RouterLink
                to="/"
                class="text-gray-700 dark:text-gray-300 hover:text-primary px-3 py-2 rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
              >
                下载
              </RouterLink>
              <RouterLink
                to="/tasks"
                class="text-gray-700 dark:text-gray-300 hover:text-primary px-3 py-2 rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
              >
                任务
              </RouterLink>
              <RouterLink
                to="/disk"
                class="text-gray-700 dark:text-gray-300 hover:text-primary px-3 py-2 rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
              >
                磁盘监控
              </RouterLink>
              <RouterLink
                to="/settings"
                class="text-gray-700 dark:text-gray-300 hover:text-primary px-3 py-2 rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
              >
                设置
              </RouterLink>
            </div>
          </div>

          <div class="flex items-center space-x-1 sm:space-x-2">
            <div class="flex md:hidden items-center space-x-1">
              <RouterLink
                to="/"
                class="p-2 text-gray-600 dark:text-gray-400 hover:text-primary rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
                title="下载"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>
              </RouterLink>
              <RouterLink
                to="/tasks"
                class="p-2 text-gray-600 dark:text-gray-400 hover:text-primary rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
                title="任务"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="8" y1="6" x2="21" y2="6"></line><line x1="8" y1="12" x2="21" y2="12"></line><line x1="8" y1="18" x2="21" y2="18"></line><line x1="3" y1="6" x2="3.01" y2="6"></line><line x1="3" y1="12" x2="3.01" y2="12"></line><line x1="3" y1="18" x2="3.01" y2="18"></line></svg>
              </RouterLink>
              <RouterLink
                to="/disk"
                class="p-2 text-gray-600 dark:text-gray-400 hover:text-primary rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
                title="磁盘监控"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="12" x2="2" y2="12"></line><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"></path><line x1="6" y1="16" x2="6.01" y2="16"></line><line x1="10" y1="16" x2="10.01" y2="16"></line></svg>
              </RouterLink>
              <RouterLink
                to="/settings"
                class="p-2 text-gray-600 dark:text-gray-400 hover:text-primary rounded-lg transition-colors"
                active-class="text-primary bg-primary/10"
                title="设置"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"></path></svg>
              </RouterLink>
            </div>

            <button 
              v-if="authStore.authEnabled"
              @click="handleLogout"
              class="text-gray-600 dark:text-gray-400 hover:text-red-500 p-2 rounded-lg transition-colors"
              title="退出登录"
            >
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"></path><polyline points="16 17 21 12 16 7"></polyline><line x1="21" y1="12" x2="9" y2="12"></line></svg>
            </button>
          </div>
        </div>
      </div>
    </nav>

    <main :class="authStore.isAuthenticated ? 'max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8' : 'w-full'">
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
</style>
