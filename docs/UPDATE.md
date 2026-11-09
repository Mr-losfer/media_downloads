# 更新机制

Media Super 有两种升级方式，互不冲突：

| 方式 | 适用 | 谁在管版本 |
|---|---|---|
| **后台「一键更新」** | 裸机、fnOS、Docker（不想动镜像） | 程序自己把新二进制写进数据目录 |
| **换镜像 / 换包** | Docker、fnOS | `docker compose pull` 或应用中心升级 |

此外还有一条独立链路：**ffmpeg 运行时组件补齐**（见第三节），它不换主程序版本，
只把 m3u8 封装需要的 ffmpeg 下载到数据目录。

---

## 一、更新检测

接口：`GET /api/system/update`，前端在进入后台时静默检查一次，5 分钟内复用缓存；点「检查更新」会带 `?force=1` 强制刷新。

元数据来源按顺序尝试，**前者失败自动降级**：

1. `https://gh-proxy.com/https://raw.githubusercontent.com/losfer778/media_downloads/main/version.json`
2. `https://raw.githubusercontent.com/losfer778/media_downloads/main/version.json`
3. `https://gh-proxy.com/https://api.github.com/repos/losfer778/media_downloads/releases/latest`
4. `https://api.github.com/repos/losfer778/media_downloads/releases/latest`

`version.json` 由打包脚本生成，内容是当前版本号、**更新内容**、各架构二进制的下载地址与 SHA256，
以及 fpk 与 ffmpeg 组件附件。`notes` 支持多行：行首 `feat:` / `fix:` / `ui:` 会渲染成分类徽章，`**重点**` 会高亮：

```json
{
  "version": "2.5.5",
  "released_at": "2026-11-09T18:20:00Z",
  "notes": "重要：修复 fpk 安装报缺少生命周期脚本\nfix: 安装回调改用多路径定位",
  "repo": "https://github.com/losfer778/media_downloads",
  "image": "ghcr.io/losfer778/media-super:2.5.5",
  "assets": {
    "linux-amd64": {
      "name": "mediasuper-linux-amd64",
      "url": "https://github.com/losfer778/media_downloads/releases/download/v2.5.5/mediasuper-linux-amd64",
      "sha256": "02eef8…",
      "size": 7430328
    }
  },
  "ffmpeg": {
    "linux-amd64": { "name": "ffmpeg-linux-amd64.gz", "url": "…/ffmpeg-linux-amd64.gz", "sha256": "…", "size": 69000000 },
    "linux-arm64": { "name": "ffmpeg-linux-arm64.gz", "url": "…/ffmpeg-linux-arm64.gz", "sha256": "…", "size": 63000000 }
  },
  "fpk": {
    "mediasuper_2.5.5_fnos-x86.fpk": { "url": "…", "sha256": "…", "size": 3212786 }
  },
  "checksums": "https://github.com/losfer778/media_downloads/releases/download/v2.5.5/SHA256SUMS"
}
```

程序按 `GOOS-GOARCH` 选择 `assets` 里对应的 key（`linux-amd64` / `linux-arm64` / `linux-armv7` / `linux-armv6` / `linux-386`），
找不到就返回 `can_auto=false`，只提示有新版本、不给一键更新按钮。
`ffmpeg` 只有 `linux-amd64` / `linux-arm64` 两个 key（对应有 fpk 的平台），其它架构走系统自带 ffmpeg。

> 为什么不用纯 GitHub API？国内访问 `api.github.com` 更容易被限流；`version.json` 走 raw 域名，
> 配合 gh-proxy 更稳定，而且能让镜像仓库与二进制版本严格对应。

### 首页新版本弹窗

进入首页检测到新版本会自动弹窗，展示 `v当前 → v最新` 与**更新内容**（分类徽章 + 重点高亮）：

- 右侧「取消更新」：本次会话不再提醒（`sessionStorage`）；
- 右侧「确认更新」：直接执行一键更新；
- 左侧「不再更新此版本」：把版本号记到浏览器 `localStorage`，**只对当前版本生效**，
  出现更高的版本仍会弹窗。

跳过记录按浏览器保存，每台设备各自独立，不影响其它客户端，也不影响设置页的手动检查 / 更新。

---

## 二、一键更新做了什么

```
POST /api/system/update
  1. 强制检查一次版本
  2. 下载对应架构的裸二进制（gh-proxy 优先，失败直连）
  3. 校验 SHA256（不一致则改用 Release API 的 digest 重试一次）
  4. 写入 <数据目录>/bin/mediasuper，并记录 bin/version
  5. 返回 {"restarting": true}
  6. 900ms 后 syscall.Exec 新二进制（PID 不变，容器不受影响）
```

要点：

- **不覆盖镜像里的文件**。Docker 里 `/usr/local/bin` 属于 root，容器用户写不了，
  所以新版本放在数据卷 `/data/bin/` 里。
- **重启后仍然生效**。每次启动会检查 `/data/bin/version`，若比当前内置版本新，
  就直接 `exec` 过去（`internal/updater.Handoff`）。
- **失败保护**。连续 3 次切换后进程没起来（`bin/handoff.fail` 计数），就跳过自动接管，
  避免容器无限重启。服务正常启动后会清掉计数。
- **摘要不一致**。`version.json` 与 Release 附件偶尔会不同步（重复发布覆盖了产物），
  此时会自动用 Releases API 里的 `digest` 再校验一次。

前端在更新期间每 1.5 秒探一次 `/api/health`，版本号变了就自动刷新页面。

---

## 三、运行时组件自动补齐（ffmpeg）

v2.5.5 起 fnOS fpk 不再内置 ffmpeg（避免 70MB 大包在旧版 fnOS 上传超时截断，
详见 [docs/FNOS.md](FNOS.md)），改由应用首次启动在后台补齐。仅 `linux-amd64` / `linux-arm64` 适用。

```
version.json
  └─ "ffmpeg": { "linux-amd64": {…}, "linux-arm64": {…} }   ← 组件 URL / SHA256 / size

启动检测系统里没有可用的 ffmpeg
  → 后台下载 ffmpeg-linux-<arch>.gz（gh-proxy 优先，失败直连）
  → 边下边用 io.TeeReader 算 SHA256，与 version.json 校验
  → gzip 流式解压到 <数据目录>/bin/ffmpeg.new
  → 校验 ELF 头 \x7fELF 与最小体积
  → 原子 rename 到 bin/ffmpeg，chmod 0755
  → 立刻生效，无需重启（candidates() 优先解析数据目录里的 bin/ffmpeg）
```

要点：

- **只下一次**。`ffRunning` 防并发重复排队，失败标记 `failed` 后不自动反复重试，避免
  「缺组件 → 反复重下整部片子」的旧问题；
- 状态通过 `GET /api/system/info` 的 `ffmpeg_state` 与 SSE `ffmpeg.state` 暴露，
  取值 `idle | downloading | installing | ready | failed | unsupported`；
- 设置 → 下载显示进度，失败可点「立即安装 / 重新安装」（`POST /api/system/ffmpeg`）手动重试；
- `version.json` 里没有 `ffmpeg` 字段时（例如旧版元数据），会按当前版本号兜底拼
  Release URL，仍能补齐；
- 本机 / 内网地址（loopback / private IP）走直连，不加 `gh-proxy` 前缀，方便本地调试。

---

## 四、自动更新

设置 → 系统更新 → 勾选「**自动更新**」，开关存进设置（`auto_update`），重启后仍然有效。

行为：

- 每 **2 小时**检查一次（进程启动 2 分钟后开始第一次巡检，之后按周期走）；
- 发现新版本后先确认**当前没有进行中或排队的任务**，再等 90 秒二次确认，
  避免用户刚粘贴链接就被重启打断；
- 如果检查时正在下载，不会反复请求网络，而是保留「待升级」状态，
  每个巡检周期（5 分钟）看一次是否空闲，空闲后立即升级；
- 升级动作与手动「一键更新」完全一致：下载 → SHA256 校验 → 写入 `数据目录/bin` → 热切换；
- 重启前会通过 SSE 推送 `update.auto`，前端提示「正在自动更新」并等待新版本恢复；
- 关掉开关后不再检查，已下载但未启动的更新仍会在下次重启时由 `Handoff` 接管。

> 调试用环境变量（生产不需要）：`MS_AUTO_UPDATE_INTERVAL=15s` 缩短检查周期，
> `MS_AUTO_UPDATE_SETTLE=0s` 跳过空闲等待。

---

## 五、回滚

### 一键更新的回滚

```bash
# 1. 停服务
docker compose stop          # 或 systemctl stop / fnOS 应用中心停止

# 2. 删掉数据目录里的更新副本
rm -rf ./data/bin

# 3. 起服务，回到镜像内置版本
docker compose start
```

`data/bin` 只放更新副本，删掉不影响任务数据（`mediasuper.db`）和下载文件。
注意 `bin/ffmpeg` 也在同一目录，删掉后下次启动会重新补齐一次。

### 镜像回滚

```bash
docker compose down
# 把 compose 里的 image 改成 :2.0.0
docker compose up -d
```

数据目录向前兼容：新版本写入的数据库旧版本也能读（只多不少字段）。

### fnOS 回滚

应用中心 → Media Super → 卸载/重装旧版 fpk；`/var/apps/mediasuper/var` 下的数据会保留。

---

## 六、手动触发（脚本 / CI）

```bash
# 检查
curl -s http://127.0.0.1:9099/api/system/update?force=1 \
  -H "Authorization: Bearer $KEY" | jq

# 更新
curl -s -X POST http://127.0.0.1:9099/api/system/update \
  -H "Authorization: Bearer $KEY"

# 手动重装 ffmpeg 组件
curl -s -X POST http://127.0.0.1:9099/api/system/ffmpeg \
  -H "Authorization: Bearer $KEY"
```

---

## 七、发布新版本（维护者）

正常流程是**推 tag，让 CI 打包**。发布前把本次「更新内容」写进仓库根目录的
`.release-notes`（每行一条，可用 `feat:` / `fix:` / `ui:` 前缀，`**重点**` 会被前端高亮）。
`build-release.sh` 把它写进 `version.json` 与 Release body，`docs/RELEASES.md` 表格只取首行做摘要，
`build-fpk.sh` 的 manifest changelog 也只取首行；没有该文件时退回最近一次 commit subject。

```bash
# 1. 改版本号
sed -i 's/Version = "2.5.4"/Version = "2.5.5"/' internal/version/version.go
git commit -am "chore: bump 2.5.5"
./scripts/git-push.sh

# 2. 打 tag（触发 .github/workflows/release.yml）
git tag v2.5.5
git push https://github.com/losfer778/media_downloads.git v2.5.5
```

CI 会：先跑 `build-fpk.sh` 出轻量 fpk 与 `ffmpeg-linux-*.gz` → 交叉编译 5 个架构 →
创建 GitHub Release 并上传全部附件（含两个 ffmpeg 组件）→ 回写 `version.json` 与
`docs/RELEASES.md`（其中 `ffmpeg` 段记录组件 URL / SHA256 / size）→ 同时构建并推送 ghcr 多架构镜像。

> `version.json` 必须由 CI 生成：里面的 SHA256 来自 CI 现场编译的二进制，
> 本地预先生成会与 Release 上的附件摘要不一致。发布完成后把 CI 回写的
> `version.json` / `docs/RELEASES.md` 同步回本地即可。

手写发布（不走 CI）：

```bash
VERSION=2.5.5 NOTES="本次更新内容" \
  ./scripts/build-release.sh --publish --commit
```

`--publish` 建 Release，`--commit` 把 `version.json` / `docs/RELEASES.md` 提交并推送，
保证「更新检测读到的摘要」和「Release 上的附件」一致。
