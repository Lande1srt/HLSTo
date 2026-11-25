#!/usr/bin/env python3
"""
HLSTo 发布目录打包脚本
======================
扫描 <root>/release 下所有平台子目录（windows-amd64、linux-arm64 等），
按平台选择常用压缩格式并输出到 <root>/release/packages/。

压缩格式映射：
  windows-*        → .zip     （Windows 原生，双击可解）
  linux-*          → .tar.gz  （Linux 发行包标配）
  darwin-*         → .tar.gz  （macOS 通用，也生成一份 .zip 方便 Finder 双击）

跨平台兼容：只用 Python 标准库 zipfile / tarfile，零依赖，Windows 上也能生成 tar.gz。

用法（三选一）：
  python package_release.py                 # 使用默认 release 目录
  python package_release.py -d H:/path     # 指定 release 目录
  python Build-Cross-Platform.ps1          # 该脚本末尾会自动调用本脚本

产物：
  release/packages/HLSTo-<version>-<os>-<arch>.zip
  release/packages/HLSTo-<version>-<os>-<arch>.tar.gz

  其中 version 取自 Build-Cross-Platform.ps1 的 $PSVersionTable.PSVersion 之外，
  默认值为 "latest"，可通过 -v 覆盖。
"""
from __future__ import annotations

import argparse
import os
import sys
import tarfile
import zipfile
from datetime import datetime
from pathlib import Path

# 默认版本号（build 脚本未传则用此值）；CI/手动传 -v 可以覆盖
DEFAULT_VERSION = "latest"
# 压缩时根目录名（解压后顶层文件夹名）
PACKAGE_ROOT = "HLSTo"


# ---------------------------------------------------------------------------
# 核心：按平台分发压缩格式
# ---------------------------------------------------------------------------

def choose_formats(os_name: str) -> list[tuple[str, str]]:
    """返回 [(后缀, mode), ...] 列表。
    suffix = ".zip" 或 ".tar.gz"
    mode   = "zip" 或 "tar.gz"
    """
    if os_name == "windows":
        return [(".zip", "zip")]
    if os_name == "darwin":
        # macOS 用户有 Finder 双击 .zip 的习惯，也提供 tar.gz 给 CI/CLI
        return [(".zip", "zip")]
    # linux 及其他 → 默认 tar.gz
    return [(".tar.gz", "tar.gz")]


# ---------------------------------------------------------------------------
# 压缩实现：纯标准库
# ---------------------------------------------------------------------------

def pack_zip(src_dir: Path, out_file: Path, root_name: str) -> Path:
    """把 src_dir 整个塞进 zip，所有条目以 root_name 为顶层前缀。"""
    out_file.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(out_file, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        for path in sorted(src_dir.rglob("*")):
            if path.is_dir():
                continue
            # 相对路径：windows-amd64/static/index.html
            rel = path.relative_to(src_dir)
            # zip 内部路径统一用正斜杠，兼容所有平台解压工具
            arcname = f"{root_name}/{rel.as_posix()}"
            zf.write(path, arcname)
    return out_file


def pack_tar_gz(src_dir: Path, out_file: Path, root_name: str) -> Path:
    """把 src_dir 整个塞进 tar.gz，所有条目以 root_name 为顶层前缀。"""
    out_file.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(out_file, "w:gz", compresslevel=9) as tf:
        for path in sorted(src_dir.rglob("*")):
            if path.is_dir():
                continue
            rel = path.relative_to(src_dir)
            arcname = f"{root_name}/{rel.as_posix()}"
            tf.add(path, arcname=arcname, recursive=False)
    return out_file


# ---------------------------------------------------------------------------
# 主流程
# ---------------------------------------------------------------------------

def iter_platform_dirs(release_root: Path):
    """遍历 release_root 下形如 <os>-<arch> 的子目录。跳过 packages/ 输出目录。"""
    for entry in sorted(release_root.iterdir()):
        if not entry.is_dir():
            continue
        if entry.name == "packages":
            continue
        if "-" not in entry.name:
            continue
        # 简单校验：必须存在一个可执行产物（后缀可空）
        any_binary = any(
            p.is_file() and p.name.endswith((".exe", ".dll"))
            or (p.is_file() and "." not in p.name)
            for p in entry.iterdir()
        )
        if not any_binary:
            continue
        yield entry


def human_size(num_bytes: int) -> str:
    if num_bytes < 1024:
        return f"{num_bytes} B"
    for unit in ("KB", "MB", "GB"):
        num_bytes /= 1024.0
        if num_bytes < 1024.0:
            return f"{num_bytes:.2f} {unit}"
    return f"{num_bytes:.2f} TB"


def build(release_dir: Path, version: str, clean: bool) -> int:
    if not release_dir.exists():
        print(f"[ERR] release 目录不存在: {release_dir}", file=sys.stderr)
        return 1

    packages_dir = release_dir / "packages"
    if clean and packages_dir.exists():
        # 只删我们生成的文件
        for old in packages_dir.glob("HLSTo-*.*"):
            old.unlink()

    successes: list[tuple[str, Path]] = []
    failures: list[str] = []

    platform_dirs = list(iter_platform_dirs(release_dir))
    if not platform_dirs:
        print(f"[WARN] {release_dir} 下没有找到任何 <os>-<arch> 平台目录")
        return 0

    print(f"[1/1] Packing release platforms → {packages_dir}")
    print(f"      version  = {version}")
    print(f"      packages = {len(platform_dirs)} 个平台目录\n")

    for plat_dir in platform_dirs:
        os_name, _, arch = plat_dir.name.partition("-")
        formats = choose_formats(os_name)

        for suffix, mode in formats:
            out_file = packages_dir / f"HLSTo-{version}-{os_name}-{arch}{suffix}"
            try:
                if mode == "zip":
                    pack_zip(plat_dir, out_file, PACKAGE_ROOT)
                elif mode == "tar.gz":
                    pack_tar_gz(plat_dir, out_file, PACKAGE_ROOT)
                else:
                    raise RuntimeError(f"未知压缩模式: {mode}")
                size = human_size(out_file.stat().st_size)
                print(f"  [OK] {out_file.name:<52s} {size}")
                successes.append((f"{os_name}-{arch}{suffix}", out_file))
            except Exception as exc:  # noqa: BLE001
                print(f"  [FAIL] {out_file.name}: {exc}", file=sys.stderr)
                failures.append(out_file.name)

    print()
    if failures:
        print(f"[DONE] 成功 {len(successes)} 个，失败 {len(failures)} 个：")
        for f in failures:
            print(f"  ✗ {f}")
        return 1
    print(f"[DONE] 全部成功，共 {len(successes)} 个发布包 → {packages_dir}")
    return 0


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(
        description="HLSTo release 目录平台自适应打包脚本（纯 Python 标准库）",
    )
    parser.add_argument(
        "-d", "--release-dir",
        default=None,
        help="release 目录路径；默认取脚本所在目录的 release/",
    )
    parser.add_argument(
        "-v", "--version",
        default=os.environ.get("BUILD_VERSION", DEFAULT_VERSION),
        help=f"版本号，默认 {DEFAULT_VERSION}（也可通过 BUILD_VERSION 环境变量注入）",
    )
    parser.add_argument(
        "--no-clean",
        action="store_true",
        help="打包前不清理 packages/ 下已存在的同名产物（默认会清）",
    )
    args = parser.parse_args()

    if args.release_dir:
        release_dir = Path(args.release_dir).resolve()
    else:
        # 脚本所在目录 → release/
        release_dir = (Path(__file__).resolve().parent / "release")

    return build(release_dir, args.version, clean=not args.no_clean)


if __name__ == "__main__":
    sys.exit(main())
