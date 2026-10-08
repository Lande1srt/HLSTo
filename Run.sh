#!/bin/bash

# ====================== 配置区（无需手动改路径） ======================
# 自动获取脚本所在目录
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
APP="${SCRIPT_DIR}/m3u8-downloader-web"
LOG="${SCRIPT_DIR}/app.log"
# ====================================================================

RED="\033[31m"
GREEN="\033[32m"
YELLOW="\033[33m"
NONE="\033[0m"

# 强制切换到程序目录，解决静态资源路径问题
cd "${SCRIPT_DIR}" || exit 1

# 检查二进制执行权限
check_perm() {
    if [ ! -x "${APP}" ]; then
        echo -e "${RED}错误：程序无执行权限，正在自动授权...${NONE}"
        chmod +x "${APP}"
        echo -e "${GREEN}权限授权完成${NONE}"
    fi
}

start() {
    check_perm

    if pgrep -f "${APP}" > /dev/null; then
        echo -e "${YELLOW}程序已在运行${NONE}"
        exit 0
    fi

    echo -e "${GREEN}正在启动后台常驻...${NONE}"
    nohup "${APP}" > "${LOG}" 2>&1 &
    sleep 1

    # 二次校验是否真的启动成功
    if pgrep -f "${APP}" > /dev/null; then
        echo -e "${GREEN}启动成功！日志查看：tail -f ${LOG}${NONE}"
    else
        echo -e "${RED}启动失败，程序意外退出，请查看日志：${LOG}${NONE}"
        exit 1
    fi
}

stop() {
    echo -e "${YELLOW}正在停止程序...${NONE}"
    pkill -f "${APP}"
    sleep 1
    echo -e "${GREEN}已停止${NONE}"
}

restart() {
    stop
    start
}

log() {
    tail -f "${LOG}"
}

status() {
    if pgrep -f "${APP}" > /dev/null; then
        echo -e "${GREEN}运行中 ✅${NONE}"
    else
        echo -e "${RED}未运行 ❌${NONE}"
    fi
}

case "$1" in
  start) start ;;
  stop) stop ;;
  restart) restart ;;
  log) log ;;
  status) status ;;
  *)
    echo "用法：$0 {start|stop|restart|log|status}"
  ;;
esac