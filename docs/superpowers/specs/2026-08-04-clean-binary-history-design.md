# 清理仓库历史中的 binary 残留 — 设计文档

日期：2026-08-04
状态：已与用户确认

## 1. 目标

把仓库 `.git` 目录从 189 MB 瘦到 < 10 MB，方法是从历史中摘除所有 binary 残留（构建产物），同时保留所有源码、配置、文档、commit message / author / date 元数据。

清理后远端 `https://github.com/54gogogo10/QoS` 仍可正常 clone、log、checkout，HEAD 内容与清理前等价（除被删的 binary 外）。

## 2. 当前问题

| 路径 | 历史中累计 | HEAD 是否在 | 大小 |
|---|---|---|---|
| `qostool-app.exe` | 36 个 blob | 是 | 326.6 MiB（pack 后约 30 MB） |
| `tools/WebView2Loader.dll` | 1 个 blob | 是 | 0.16 MiB |
| `dist/qostool-v2.0.0/{qostool-app.exe,WebView2Loader.dll}` | 2 个 blob | 否（`7277838` 移出） | 17.5 MiB |
| **合计** | | | **~50 MB pack 后** |

`.gitignore` 已含 `dist/` 和 `qostool.exe`，但漏了 `qostool-app.exe`（带 `-app` 后缀）和 `tools/*.dll`，导致这些 binary 持续入库并被 60+ 个 commit 反复携带。

## 3. 范围

**移除**（filter-repo `--invert-paths`）：
- `qostool-app.exe`
- `tools/WebView2Loader.dll`
- `dist/qostool-v2.0.0/*.exe` / `*.dll`
- `dist/qostool-v2.0.1/*.exe` / `*.dll`
- `dist/qostool-v2.0.2/*.exe` / `*.dll`

**保留**：
- 所有 Go 源码（`cmd/`、`internal/`）
- 配置（`configs/`）
- 文档（`docs/`、`README.md`、`build.sh`）
- commit 数量（66 个不变）
- commit message / author / date / committer（全部保留）

**不变**：
- 仓库地址、远端、协作者权限
- 仓库 description / topics / secret scanning 设置（最后恢复 push protection）

## 4. 实施步骤

| # | 动作 | 命令 / 备注 |
|---|---|---|
| 1 | 关 secret scanning push protection | `gh api -X PATCH ... secret_scanning_push_protection[status]=disabled`（避免 push 拦下） |
| 2 | Bare clone 备份 | `git clone --bare . ../QoS-backup-$(date +%Y%m%d)` |
| 3 | 装 git-filter-repo | `pip install git-filter-repo`（环境无则装） |
| 4 | 改 `.gitignore` | 追加 `qostool-app.exe` 和 `tools/*.dll` |
| 5 | filter-repo 重写历史 | `git filter-repo --invert-paths --path qostool-app.exe --path tools/WebView2Loader.dll --path-glob 'dist/qostool-v2.0.*/*.{exe,dll}'` |
| 6 | 释放空间 | `git reflog expire --expire=now --all && git gc --prune=now --aggressive` |
| 7 | 本地验证 | `git fsck` 通过 / `git log --oneline` 仍 66 commits / 上述 binary 不在树中 / `du -sh .git` < 10 MB |
| 8 | 提交 .gitignore | `git add .gitignore && git -c user.name=... commit -m "chore(gitignore): 排除 qostool-app.exe 与 tools/*.dll"` |
| 9 | force push | `git push --force-with-lease origin main`（用 `--force-with-lease` 保护：如果远端有未感知的新 commit 会拒绝） |
| 10 | 远端验证 | `gh api repos/.../branches/main` / `.../git/trees/HEAD?recursive=1` / 看 size |
| 11 | 恢复 push protection | `gh api -X PATCH ... secret_scanning_push_protection[status]=enabled` |

## 5. 关键设计决策

- **`--force-with-lease` 而非 `--force`**：远端如有未感知的新提交会拒绝覆盖。仓库刚由本人 push，无其他协作者，但仍按 best practice 用。
- **filter-repo 改所有 commit hash**：因为 commit 内容变了（binary 被摘除），66 个 commit 的 SHA 全部变化。这正常且必要。
- **.gitignore 单独一个 commit** 推上去：与"清理历史"是因果关系但职责不同，分开审计更清楚。
- **先 filter 本地验证 → 再 push**：避免远端变成"半残"状态。
- **保留原 GitHub 仓库的 default_branch / description / topics / secret scanning 设置**：除了临时关 push protection 外，其他配置不动。

## 6. 风险与回滚

| 风险 | 概率 | 缓解 |
|---|---|---|
| 远端 push 失败 | 低 | 重试；用 `--force-with-lease` 保护 |
| 误删文件（filter-repo path 写错） | 低 | filter-repo 实际跑前会列受影响路径；并有 backup bare clone |
| 推送后其他人 rebase 困难 | 不适用 | 单人仓库，无其他协作者 |
| 仓库 hash 改变导致本地旧 ref 失效 | 必然 | 本地 push 后会自动更新；这是已知代价 |

**回滚方案**：
- 如果 push 完发现有问题：删除本地 `D:/pi/QoS` → 从 `../QoS-backup-YYYYMMDD` clone 回来 → 远端 `git push --force --all` 覆盖
- 如果是 filter-repo 步骤本身出问题：直接 `rm -rf .git && git init` + 从 backup clone 重建

## 7. 验证标准（Done Definition）

- [ ] `du -sh .git` < 10 MB（本地）
- [ ] `gh api repos/54gogogo10/QoS` 中 `size` 字段 < 100（KB 单位）
- [ ] `git log --oneline | wc -l` = 66（commits 数不变）
- [ ] `git ls-files | grep -E '\.(exe|dll)$'` 为空（HEAD 树无 binary）
- [ ] `git rev-list --all --objects | awk '$2 ~ /\.(exe|dll)$/'` 为空（历史无 binary）
- [ ] `git fsck` 通过
- [ ] 远端 main HEAD commit message 为 `feat(sender): Windows 非管理员发送非零 DSCP 时启动警告（Win7 场景）`
- [ ] `gh api repos/.../git/trees/HEAD?recursive=1 | grep -E '(exe|dll)'` 无匹配
- [ ] secret scanning push protection 已重新启用
- [ ] backup bare clone 仍存在于 `../QoS-backup-YYYYMMDD`

## 8. 不做

- 不用 `git filter-branch`（已弃用、性能差、API 不稳）
- 不改 Go 源码
- 不动 commit author / date
- 不动 build.sh / docs / 配置
- 不引入 Git LFS（用户选择 A 方案）
- 不删 backup（手动决定）
