/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<{}, {}, any>
  export default component
}

// Wails 类型声明
interface Window {
  go: {
    main: {
      App: {
        Greet(name: string): Promise<string>
        GetVersion(): Promise<string>
      }
    }
  }
}
