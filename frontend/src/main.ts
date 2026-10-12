import { createApp } from 'vue'
import { createPinia } from 'pinia'
import router from './router'
import App from './App.vue'
import { initRuntime } from './api/runtime'
import { applyApiBase } from './api'
import './assets/main.css'
import './assets/dark-mode.css'

// 桌面端先拿到本地 API 服务端口，再挂载应用，保证首屏请求地址正确
initRuntime().finally(() => {
  applyApiBase()

  const app = createApp(App)

  app.use(createPinia())
  app.use(router)

  app.mount('#app')
})
