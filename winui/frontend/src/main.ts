// @ts-nocheck
// Wails 前端入口文件

// 等待 DOM 加载完成
document.addEventListener('DOMContentLoaded', async () => {
  const app = document.getElementById('app');
  if (app) {
    // 尝试获取版本信息
    try {
      if (window.go && window.go.main && window.go.main.App) {
        const version = await window.go.main.App.GetVersion();
        console.log('HLSTo 版本:', version);
      }
    } catch (e) {
      console.log('Wails bindings not available in dev mode');
    }
    
    // 显示加载提示
    app.innerHTML = `
      <div style="display: flex; justify-content: center; align-items: center; height: 100vh; flex-direction: column; background: #1a1a2e; color: white; font-family: system-ui, sans-serif;">
        <h1>HLSTo - M3U8 下载器</h1>
        <p>正在加载...</p>
        <p style="color: #888; font-size: 12px;">请使用浏览器访问 http://localhost:5173 进行开发调试</p>
      </div>
    `;
  }
});
