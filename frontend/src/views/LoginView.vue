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
  fetch('https://v1.hitokoto.cn/')
    .then(response => response.json())
    .then(data => {
      if (data.hitokoto) {
        hitokoto.value = data.hitokoto
      }
    })
    .catch(() => {})
})
</script>

<template>
  <div class="login-container">
    <div class="right-panel">
      <div class="login-modal">
        <div class="modal-header">
          <h2>HLSTo</h2>
          <p class="hitokoto-text">{{ hitokoto }}</p>
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
            {{ loading ? '登录中...' : '立即登录' }}
          </button>
        </form>

        <div class="copyright">
          <p>© {{ new Date().getFullYear() }} Coldsea Team. All rights reserved.</p>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-container {
  display: flex;
  min-height: 100vh;
  background: #ecf0f1;
}

.right-panel {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 2rem;
}

.login-modal {
  width: 100%;
  max-width: 420px;
  background: #ffffff;
  border-radius: 6px;
  padding: 3rem;
  box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
}

.modal-header {
  text-align: center;
  margin-bottom: 2rem;
}

.modal-header h2 {
  font-size: 2rem;
  font-weight: 700;
  color: #2c3e50;
  margin-bottom: 0.5rem;
}

.hitokoto-text {
  color: #7f8c8d;
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
  color: #2c3e50;
}

.input-wrapper {
  position: relative;
  display: flex;
  align-items: center;
}

.input-icon {
  position: absolute;
  left: 1rem;
  width: 20px;
  height: 20px;
  color: #7f8c8d;
  pointer-events: none;
}

.input-wrapper input {
  width: 100%;
  padding: 0.9rem 1rem 0.9rem 3rem;
  border: 1px solid #bdc3c7;
  border-radius: 4px;
  font-size: 1rem;
  transition: border-color 0.2s ease;
  background: #ffffff;
  color: #2c3e50;
}

.input-wrapper input:focus {
  outline: none;
  border-color: #3498db;
}

.toggle-password-btn {
  position: absolute;
  right: 0.75rem;
  background: none;
  border: none;
  padding: 0.5rem;
  cursor: pointer;
  color: #7f8c8d;
  display: flex;
  align-items: center;
}

.toggle-password-btn:hover {
  color: #3498db;
}

.eye-icon {
  width: 20px;
  height: 20px;
}

.error-message {
  color: #e74c3c;
  font-size: 0.875rem;
  text-align: center;
  background: rgba(231, 76, 60, 0.1);
  padding: 0.75rem;
  border-radius: 4px;
  border: 1px solid rgba(231, 76, 60, 0.2);
}

.login-button {
  width: 100%;
  padding: 1rem 2rem;
  background: #3498db;
  color: #ffffff;
  border: none;
  border-radius: 4px;
  font-size: 1.05rem;
  font-weight: 600;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background 0.2s ease;
}

.login-button:hover {
  background: #2980b9;
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
  margin-left: 0.5rem;
}

.copyright {
  margin-top: 2rem;
  text-align: center;
  font-size: 0.85rem;
  color: #7f8c8d;
  padding-top: 1.5rem;
  border-top: 1px solid #ecf0f1;
}

@keyframes spin {
  to { transform: rotate(360deg); }
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
