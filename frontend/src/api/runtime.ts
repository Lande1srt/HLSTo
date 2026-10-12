/**
 * 运行时环境探测
 *
 * - 浏览器 / 独立服务部署（vite dev、docker、本地后端）：API 与 WebSocket 使用相对路径
 * - Wails 桌面端（WinUI）：Wails AssetServer 不支持 WebSocket 升级，
 *   后端会在 127.0.0.1 随机端口启动真实 HTTP 服务，通过绑定方法 GetServerBaseURL 暴露；
 *   前端初始化时获取该地址，REST API / WebSocket 全部直连该端口。
 */

// Wails v2 会向 window 注入 go 绑定对象，桌面环境下该调用存在
interface WailsAppBinding {
  GetServerBaseURL?: () => Promise<string>
}
interface WailsGlobal {
  main?: { App?: WailsAppBinding }
}

declare global {
  interface Window {
    go?: WailsGlobal
  }
}

/** http(s) 基址，如 http://127.0.0.1:51234；浏览器环境为空串（走相对路径） */
let httpBase = ''
/** ws(s) 基址，如 ws://127.0.0.1:51234；浏览器环境为空串 */
let wsBase = ''

const sleep = (ms: number): Promise<void> => new Promise(resolve => setTimeout(resolve, ms))

/** 探活本地服务，服务随 OnStartup 异步 Serve，可能比前端首请求晚就绪 */
async function waitServerReady(base: string, maxAttempts = 20): Promise<boolean> {
  for (let i = 0; i < maxAttempts; i++) {
    try {
      // 用 no-cors 轻量探活：即使被鉴权拦截，只要 TCP/HTTP 有响应即视为就绪
      await fetch(`${base}/api/auth/check`, { method: 'GET', cache: 'no-store' })
      return true
    } catch {
      await sleep(100)
    }
  }
  return false
}

/** 应用挂载前调用一次：桌面环境拉取本地服务端口 */
export async function initRuntime(): Promise<void> {
  try {
    const binding = window.go?.main?.App?.GetServerBaseURL
    if (typeof binding === 'function') {
      const base = ((await binding()) || '').replace(/\/+$/, '')
      if (/^https?:\/\//.test(base)) {
        const ready = await waitServerReady(base)
        if (ready) {
          httpBase = base
          wsBase = base.replace(/^http/, 'ws')
        } else {
          console.warn('[runtime] 桌面端本地服务未就绪，回退相对路径')
        }
      }
    }
  } catch (err) {
    console.warn('[runtime] 获取桌面端本地服务地址失败，回退相对路径:', err)
    httpBase = ''
    wsBase = ''
  }
}

export const getHttpBase = (): string => httpBase
export const getWsBase = (): string => wsBase

/** 构造 WebSocket 地址；浏览器环境回退到当前页面主机 */
export function buildWsUrl(pathWithQuery: string): string {
  if (wsBase) {
    return `${wsBase}${pathWithQuery}`
  }
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}${pathWithQuery}`
}
