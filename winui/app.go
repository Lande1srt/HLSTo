package main

import (
	"context"
	"log"
)

// App 结构体 - Wails 绑定的主要应用
type App struct {
	ctx context.Context
}

// NewApp 创建新的应用实例
func NewApp() *App {
	return &App{}
}

// startup 在应用启动时调用
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	log.Println("HLSTo 应用程序已启动")
}

// beforeClose 在应用关闭前调用
func (a *App) beforeClose(ctx context.Context) bool {
	log.Println("HLSTo 应用程序即将关闭")
	return false
}

// Greet 演示 Wails 绑定方法
func (a *App) Greet(name string) string {
	return "Hello, " + name + "! 欢迎使用 HLSTo!"
}

// GetVersion 返回应用版本
func (a *App) GetVersion() string {
	return "1.0.0"
}
