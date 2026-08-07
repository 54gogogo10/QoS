#!/bin/bash
# qostool 全平台一键构建脚本
# 产物：Windows(Win10/11) app+CLI / Linux amd64+arm64 / Windows 7
# 用法：bash build.sh [版本号]   （版本号默认 v2.7.0，用于 dist 目录名）
set -e
cd "$(dirname "$0")"

VERSION="${1:-v2.7.0}"
export CGO_ENABLED=1
export CGO_CFLAGS="-IC:/WpdPack/Include"
export CGO_LDFLAGS="-LC:/WpdPack/Lib/x64 -lwpcap"

echo "========== qostool $VERSION 全平台构建 =========="

echo "=== 1/5 Windows (Win10/11) ==="
export PATH="/c/go/bin:$PATH"
go build -trimpath -ldflags "-s -w" -o qostool.exe ./cmd/qostool
go build -trimpath -ldflags "-s -w -H windowsgui" -o qostool-app.exe ./cmd/qostool
echo "  OK: qostool.exe / qostool-app.exe"

echo "=== 2/5 Linux amd64 (麒麟/通用) ==="
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o qostool-linux-amd64 ./cmd/qostool
echo "  OK: qostool-linux-amd64"

echo "=== 3/5 Linux arm64 (麒麟/鲲鹏飞腾) ==="
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o qostool-linux-arm64 ./cmd/qostool
echo "  OK: qostool-linux-arm64"

echo "=== 4/5 Windows 7 (Go 1.20 + 降级依赖) ==="
if [ ! -x /c/go1.20/go/bin/go.exe ]; then
  echo "错误: 未找到 Go 1.20 工具链 (C:\\go1.20\\go)。请先安装:"
  echo "  curl -L -o /c/go1.20.zip https://dl.google.com/go/go1.20.14.windows-amd64.zip"
  echo "  unzip -q -o /c/go1.20.zip -d /c/go1.20 && rm /c/go1.20.zip"
  exit 1
fi
# 临时降级 go.mod（Win7 需要 Go 1.20 与旧依赖）；任何失败都必须恢复，否则仓库损坏
cp go.mod go.mod.bak
cp go.sum go.sum.bak
restore_mod() { mv -f go.mod.bak go.mod 2>/dev/null || true; mv -f go.sum.bak go.sum 2>/dev/null || true; }
trap restore_mod EXIT
PY=python
command -v python >/dev/null 2>&1 || PY=python3
$PY - <<'PYEOF'
s = open('go.mod', encoding='utf-8').read()
s = s.replace('go 1.26.5', 'go 1.20')
s = s.replace('github.com/gopacket/gopacket v1.7.0', 'github.com/gopacket/gopacket v1.2.0')
s = s.replace('golang.org/x/sys v0.45.0', 'golang.org/x/sys v0.13.0')
open('go.mod', 'w', encoding='utf-8').write(s)
print('  go.mod 临时降级完成')
PYEOF
# 校验降级确实生效（go.mod 版本升级后 sed 精确匹配会静默漏替换，在此显式失败）
grep -q '^go 1.20$' go.mod || { echo "错误: go.mod 降级失败（go 版本行不是 'go 1.20'）"; exit 1; }
grep -q 'gopacket/gopacket v1.2.0' go.mod || { echo "错误: gopacket 降级失败"; exit 1; }
grep -q 'x/sys v0.13.0' go.mod || { echo "错误: x/sys 降级失败"; exit 1; }
export PATH="/c/go1.20/go/bin:$PATH"
if ! CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go mod tidy; then
  echo "错误: go mod tidy 失败（Win7 依赖与 Go 1.20 不兼容？）"
  exit 1
fi
if ! CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o qostool-win7.exe ./cmd/qostool; then
  echo "错误: Win7 构建失败"
  exit 1
fi
restore_mod
trap - EXIT
echo "  OK: qostool-win7.exe (依赖已恢复)"

echo "=== 5/5 打包 dist/$VERSION ==="
rm -rf "dist/$VERSION"
mkdir -p "dist/$VERSION"
# WebView2Loader.dll 不在仓库（.gitignore 排除 tools/*.dll），缺失时跳过（Win7 CLI 不需要）
FILES="qostool-app.exe qostool.exe qostool-linux-amd64 qostool-linux-arm64 qostool-win7.exe configs/example.yaml README.md"
if [ -f tools/WebView2Loader.dll ]; then
  FILES="$FILES tools/WebView2Loader.dll"
else
  echo "  WARN: 未找到 tools/WebView2Loader.dll（app 版内嵌窗口依赖它），已跳过打包"
fi
cp $FILES "dist/$VERSION/"
VERSION="$VERSION" $PY - <<'PYEOF'
import zipfile, os
ver = os.environ['VERSION']
with zipfile.ZipFile(f'dist/{ver}-win64.zip', 'w', zipfile.ZIP_DEFLATED) as z:
    for f in os.listdir(f'dist/{ver}'):
        z.write(os.path.join(f'dist/{ver}', f), os.path.join(ver, f))
print(f'  zip: dist/{ver}-win64.zip')
PYEOF
rm -f qostool-linux-amd64 qostool-linux-arm64 qostool-win7.exe
echo ""
echo "========== 构建完成 =========="
ls -la "dist/$VERSION/"
ls -la "dist/$VERSION-win64.zip"
