import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { public: true }
  },
  {
    path: '/',
    name: 'download',
    component: () => import('@/views/DownloadView.vue')
  },
  {
    path: '/tasks',
    name: 'tasks',
    component: () => import('@/views/TaskListView.vue')
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('@/views/SettingsView.vue')
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

// 路由守卫
router.beforeEach(async (to, _from, next) => {
  const authStore = useAuthStore()
  
  // 每次路由跳转前检查一次 authEnabled 状态
  await authStore.checkAuth()

  if (authStore.authEnabled && !authStore.isAuthenticated && !to.meta.public) {
    // 开启了鉴权且未登录，且不是公开页面
    next({ name: 'login' })
  } else if (authStore.isAuthenticated && to.name === 'login') {
    // 已登录但想去登录页，重定向到首页
    next({ name: 'download' })
  } else {
    next()
  }
})

export default router
