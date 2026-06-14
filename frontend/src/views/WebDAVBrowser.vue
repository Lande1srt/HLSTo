<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { settingsAPI } from '@/api'

const props = defineProps<{
  show: boolean
  url: string
  username?: string
  password?: string
  initialPath: string
}>()

const emit = defineEmits(['close', 'select'])

const currentPath = ref(props.initialPath || '/')
const files = ref<any[]>([])
const loading = ref(false)
const error = ref('')

const fetchDirs = async (path: string) => {
  loading.value = true
  error.value = ''
  try {
    const res = await settingsAPI.listWebDAVDir({
      url: props.url,
      username: props.username,
      password: props.password,
      path: path
    })
    if (res.data.code === 200) {
      files.value = res.data.data || []
      currentPath.value = path
    } else {
      error.value = res.data.message || '获取目录失败'
    }
  } catch (err: any) {
    error.value = err.response?.data?.message || '网络请求失败'
  } finally {
    loading.value = false
  }
}

const navigateTo = (path: string) => {
  fetchDirs(path)
}

const goBack = () => {
  if (currentPath.value === '/' || currentPath.value === '') return
  const parts = currentPath.value.split('/').filter(Boolean)
  parts.pop()
  const parentPath = '/' + parts.join('/')
  fetchDirs(parentPath)
}

const selectCurrent = () => {
  emit('select', currentPath.value)
  emit('close')
}

onMounted(() => {
  if (props.show) {
    fetchDirs(currentPath.value)
  }
})
</script>

<template>
  <div v-if="show" class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
    <div class="w-full max-w-lg bg-white rounded-lg shadow-lg overflow-hidden flex flex-col max-h-[80vh]">
      <div class="px-6 py-4 border-b border-gray-300 flex items-center justify-between">
        <h3 class="text-lg font-bold text-gray-800">选择远程目录</h3>
        <button @click="emit('close')" class="text-gray-500 hover:text-gray-700 transition-colors">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
        </button>
      </div>

      <div class="px-6 py-3 bg-gray-100 border-b border-gray-300 flex items-center gap-2 overflow-x-auto">
        <button @click="navigateTo('/')" class="text-primary hover:underline text-sm font-medium whitespace-nowrap">根目录</button>
        <template v-for="(part, i) in currentPath.split('/').filter(Boolean)" :key="i">
          <span class="text-gray-500">/</span>
          <button 
            @click="navigateTo('/' + currentPath.split('/').filter(Boolean).slice(0, i + 1).join('/'))"
            class="text-primary hover:underline text-sm font-medium whitespace-nowrap"
          >
            {{ part }}
          </button>
        </template>
      </div>

      <div class="flex-1 overflow-y-auto p-2">
        <div v-if="loading" class="flex flex-col items-center justify-center py-12 text-gray-500">
          <div class="w-8 h-8 border-2 border-primary border-t-transparent rounded-full animate-spin mb-4"></div>
          <p>正在读取目录...</p>
        </div>

        <div v-else-if="error" class="p-4 text-center">
          <p class="text-red-500 mb-4">{{ error }}</p>
          <button @click="fetchDirs(currentPath)" class="btn-secondary py-1 px-4">重试</button>
        </div>

        <div v-else class="space-y-1">
          <button 
            v-if="currentPath !== '/'"
            @click="goBack"
            class="w-full flex items-center gap-3 px-4 py-2 hover:bg-gray-100 rounded-lg text-gray-700 transition-colors text-left"
          >
            <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-gray-500"><path d="m15 18-6-6 6-6"/></svg>
            <span>返回上级目录</span>
          </button>

          <button 
            v-for="file in files" 
            :key="file.path"
            @click="navigateTo(file.path)"
            class="w-full flex items-center gap-3 px-4 py-2 hover:bg-gray-100 rounded-lg text-gray-700 transition-colors text-left"
          >
            <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-primary"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg>
            <span>{{ file.name }}</span>
          </button>

          <div v-if="!files || files.length === 0" class="py-12 text-center text-gray-500">
            <p>该目录下没有子目录</p>
          </div>
        </div>
      </div>

      <div class="p-6 border-t border-gray-300 flex items-center justify-between gap-4">
        <div class="flex-1 min-w-0">
          <p class="text-xs text-gray-500 uppercase font-bold tracking-wider mb-1">当前选择</p>
          <p class="text-sm text-gray-800 truncate font-mono">{{ currentPath }}</p>
        </div>
        <div class="flex gap-3">
          <button @click="emit('close')" class="btn-secondary py-2">取消</button>
          <button @click="selectCurrent" class="btn-primary py-2 px-6">确定选择</button>
        </div>
      </div>
    </div>
  </div>
</template>
