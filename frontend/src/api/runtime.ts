/**
 * 运行时环境探测
 *
 * - 浏览器 / 独立服务部署（vite dev、docker、本地后端）：API 与 WebSocket 使用相对路径
 * - Wails 桌面端（WinUI）：Wails AssetServer 不支持 WebSocket 升级，
 *   后端会在 127.0.0.1 随机端口启动真实 HTTP 服务；
 *   前端通过 HTTP 路由 /api/wails/server-info 拿到该地址后直连，
 *   REST 与 WebSocket 都不再走 AssetServer。
 */

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

/** 桌面端获取本地服务地址（REST + WebSocket 真实端口）。
 *
 * 走 HTTP 路由 `/api/wails/server-info`：main.go 已把 router 挂到 AssetServer.Handler，
 * 相对路径请求会被路由到 serverInfoHandler，直接返回真实端口。
 * 不依赖 Wails 的 Go 绑定生成器（v2.17+ 会静默过滤部分公开方法）。
 * 浏览器环境下该路由不存在，fetch 失败即返回 null，上层保持相对路径。
 */
async function fetchServerBaseURL(): Promise<string | null> {
  try {
    const resp = await fetch('/api/wails/server-info', { cache: 'no-store' })
    if (!resp.ok) return null
    const json = (await resp.json()) as { baseURL?: string }
    return typeof json.baseURL === 'string' && json.baseURL.trim() ? json.baseURL : null
  } catch {
    return null
  }
}

/** 应用挂载前调用一次：桌面环境拉取本地服务端口 */
export async function initRuntime(): Promise<void> {
  try {
    const base = await fetchServerBaseURL()
    if (base) {
      const trimmed = base.replace(/\/+$/, '')
      if (/^https?:\/\//.test(trimmed)) {
        const ready = await waitServerReady(trimmed)
        if (ready) {
          httpBase = trimmed
          wsBase = trimmed.replace(/^http/, 'ws')
          return
        }
      }
    }
    console.warn('[runtime] 桌面端本地服务未就绪，回退相对路径')
  } catch (err) {
    console.warn('[runtime] 获取桌面端本地服务地址失败，回退相对路径:', err)
  }
  httpBase = ''
  wsBase = ''
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
