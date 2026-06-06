<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const authStore = useAuthStore()

const username = ref('')
const password = ref('')
const showPassword = ref(false)
const loading = ref(false)
const error = ref('')
const hitokoto = ref('生活就像一盒巧克力，你永远不知道下一块是什么味道')

const togglePassword = () => {
  showPassword.value = !showPassword.value
}

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

onMounted(() => {
  // 加载一言
  fetch('https://v1.hitokoto.cn/')
    .then(response => response.json())
    .then(data => {
      if (data.hitokoto) {
        hitokoto.value = data.hitokoto
      }
    })
    .catch(() => {
      // 保持默认值
    })
})
</script>

<template>
  <div class="login-container">
    <!-- 左侧图片区域 -->
    <div class="left-panel">
      <div class="overlay">
        <div class="brand">
          <h1>HLSTo</h1>
          <p class="hitokoto-text">{{ hitokoto }}</p>
        </div>
      </div>
    </div>

    <!-- 右侧登录表单区域 -->
    <div class="right-panel">
      <div class="login-modal">
        <div class="modal-header">
          <h2>登录</h2>
          <p>请验证您的管理员身份</p>
        </div>

        <form class="login-form" @submit.prevent="handleLogin">
          <div class="form-group">
            <label for="username">管理员账号</label>
            <div class="input-wrapper">
              <svg class="input-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"></path>
                <circle cx="12" cy="7" r="4"></circle>
              </svg>
              <input
                v-model="username"
                type="text"
                id="username"
                placeholder="请输入账号"
                required
                autocomplete="username"
              />
            </div>
          </div>

          <div class="form-group">
            <label for="password">密码</label>
            <div class="input-wrapper">
              <svg class="input-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <rect x="3" y="11" width="18" height="11" rx="2" ry="2"></rect>
                <path d="M7 11V7a5 5 0 0 1 10 0v4"></path>
              </svg>
              <input
                v-model="password"
                :type="showPassword ? 'text' : 'password'"
                id="password"
                placeholder="请输入密码"
                required
                autocomplete="current-password"
              />
              <button type="button" class="toggle-password-btn" @click="togglePassword">
                <svg v-if="!showPassword" class="eye-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
                <svg v-else class="eye-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"></path>
                  <line x1="1" y1="1" x2="23" y2="23"></line>
                </svg>
              </button>
            </div>
          </div>

          <div v-if="error" class="error-message">
            {{ error }}
          </div>

          <button type="submit" class="login-button" :class="{ 'loading': loading }" :disabled="loading">
            <span v-if="!loading">立即登录</span>
            <svg v-if="!loading" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M5 12h14M12 5l7 7-7 7"></path>
            </svg>
          </button>
        </form>

        <div class="copyright">
          <p>© {{ new Date().getFullYear() }} <span class="frizon-font">Coldsea</span> Team. All rights reserved.</p>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-container {
  display: flex;
  min-height: 100vh;
  background: #ffffff;
}

/* 左侧面板 */
.left-panel {
  flex: 1;
  position: relative;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  display: none;
  overflow: hidden;
}

.left-panel::before {
  content: "";
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-image: url("https://rpic.coldsea.vip/");
  background-size: cover;
  background-position: center;
  filter: brightness(0.8);
}

.overlay {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: linear-gradient(135deg, rgba(0, 98, 255, 0.8) 0%, rgba(154, 0, 193, 0.8) 100%);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 3rem;
}

.brand {
  text-align: center;
  color: #ffffff;
  animation: fadeInUp 0.8s ease-out;
}

.brand h1 {
  font-size: 3rem;
  font-weight: 700;
  margin-bottom: 1rem;
  text-shadow: 0 2px 10px rgba(0, 0, 0, 0.2);
}

.hitokoto-text {
  font-size: 1.25rem;
  opacity: 0.95;
  font-weight: 400;
}

/* 右侧面板 */
.right-panel {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 2rem;
  background: #f9f9f9;
}

.login-modal {
  width: 100%;
  max-width: 420px;
  background: #ffffff;
  border-radius: 20px;
  padding: 3rem;
  box-shadow: 0 16px 32px rgba(0, 0, 0, 0.16);
  animation: fadeInUp 0.6s ease-out;
}

.modal-header {
  text-align: center;
  margin-bottom: 2rem;
}

.modal-header h2 {
  font-size: 2rem;
  font-weight: 700;
  color: #1e293b;
  margin-bottom: 0.5rem;
}

.modal-header p {
  color: #64748b;
  font-size: 0.95rem;
}

.login-form {
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
}

.form-group {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.form-group label {
  font-size: 0.9rem;
  font-weight: 600;
  color: #1e293b;
}

.input-wrapper {
  position: relative;
  display: flex;
  align-items: center;
  transition: all 0.3s ease;
}

.input-icon {
  position: absolute;
  left: 1rem;
  width: 20px;
  height: 20px;
  color: #64748b;
  pointer-events: none;
}

.input-wrapper input {
  width: 100%;
  padding: 0.9rem 1rem 0.9rem 3rem;
  border: 2px solid #e2e8f0;
  border-radius: 12px;
  font-size: 1rem;
  transition: all 0.3s ease;
  background: #ffffff;
  color: #1e293b;
}

.input-wrapper input:focus {
  outline: none;
  border-color: #6366f1;
  box-shadow: 0 0 0 4px rgba(99, 102, 241, 0.1);
}

.toggle-password-btn {
  position: absolute;
  right: 0.75rem;
  background: none;
  border: none;
  padding: 0.5rem;
  cursor: pointer;
  color: #64748b;
  display: flex;
  align-items: center;
}

.toggle-password-btn:hover {
  color: #6366f1;
}

.eye-icon {
  width: 20px;
  height: 20px;
}

.error-message {
  color: #ef4444;
  font-size: 0.875rem;
  text-align: center;
  background: rgba(239, 68, 68, 0.1);
  padding: 0.75rem;
  border-radius: 8px;
  border: 1px solid rgba(239, 68, 68, 0.2);
}

.login-button {
  width: 100%;
  padding: 1rem 2rem;
  background: linear-gradient(135deg, #6366f1 0%, #4f46e5 100%);
  color: #ffffff;
  border: none;
  border-radius: 12px;
  font-size: 1.05rem;
  font-weight: 600;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
  transition: all 0.3s ease;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
}

.login-button:hover {
  transform: translateY(-2px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
}

.login-button svg {
  width: 20px;
  height: 20px;
  transition: transform 0.3s ease;
}

.login-button:hover svg {
  transform: translateX(4px);
}

.login-button.loading {
  opacity: 0.7;
  cursor: not-allowed;
}

.login-button.loading::after {
  content: "";
  width: 20px;
  height: 20px;
  border: 2px solid transparent;
  border-top-color: #ffffff;
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
}

.copyright {
  margin-top: 2rem;
  text-align: center;
  font-size: 0.85rem;
  color: #64748b;
  padding-top: 1.5rem;
  border-top: 1px solid #e2e8f0;
}

.frizon-font {
  font-family: 'frizon', sans-serif;
  font-weight: bold;
}

@keyframes fadeInUp {
  from {
    opacity: 0;
    transform: translateY(30px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

/* 响应式设计 */
@media (min-width: 768px) {
  .left-panel {
    display: block;
  }
  .right-panel {
    max-width: 600px;
  }
}

@media (max-width: 480px) {
  .login-modal {
    padding: 2rem;
  }
  .modal-header h2 {
    font-size: 1.75rem;
  }
}
</style>
