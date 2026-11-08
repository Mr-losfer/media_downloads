# 更新机制

Media Super 有两种升级方式，互不冲突：

| 方式 | 适用 | 谁在管版本 |
|---|---|---|
| **后台「一键更新」** | 裸机、fnOS、Docker（不想动镜像） | 程序自己把新二进制写进数据目录 |
| **换镜像 / 换包** | Docker、fnOS | `docker compose pull` 或应用中心升级 |

---

## 一、更新检测

接口：`GET /api/system/update`，前端在进入后台时静默检查一次，5 分钟内复用缓存；点「检查更新」会带 `?force=1` 强制刷新。

元数据来源按顺序尝试，**前者失败自动降级**：

1. `https://gh-proxy.com/https://raw.githubusercontent.com/losfer778/media_downloads/main/version.json`
2. `https://raw.githubusercontent.com/losfer778/media_downloads/main/version.json`
3. `https://gh-proxy.com/https://api.github.com/repos/losfer778/media_downloads/releases/latest`
4. `https://api.github.com/repos/losfer778/media_downloads/releases/latest`

`version.json` 由打包脚本生成，内容是当前版本号、更新说明、各架构二进制的下载地址与 SHA256：

```json
{
  "version": "2.1.0",
  "released_at": "2026-11-08T14:05:54Z",
  "notes": "更新说明",
  "assets": {
    "linux-amd64": {
      "name": "mediasuper-linux-amd64",
      "url": "https://github.com/losfer778/media_downloads/releases/download/v2.1.0/mediasuper-linux-amd64",
      "sha256": "02eef8…",
      "size": 7344312
    }
  }
}
```

程序按 `GOOS-GOARCH` 选择对应的 key（`linux-amd64` / `linux-arm64` / `linux-armv7` / `linux-armv6` / `linux-386`），
找不到就返回 `can_auto=false`，只提示有新版本、不给一键更新按钮。

> 为什么不用纯 GitHub API？国内访问 `api.github.com` 更容易被限流；`version.json` 走 raw 域名，
> 配合 gh-proxy 更稳定，而且能让镜像仓库与二进制版本严格对应。

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

## 三、自动更新

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

## 四、回滚

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

## 五、手动触发（脚本 / CI）

```bash
# 检查
curl -s http://127.0.0.1:9099/api/system/update?force=1 \
  -H "Authorization: Bearer $KEY" | jq

# 更新
curl -s -X POST http://127.0.0.1:9099/api/system/update \
  -H "Authorization: Bearer $KEY"
```

---

## 六、发布新版本（维护者）

正常流程是**推 tag，让 CI 打包**：

```bash
# 1. 改版本号
sed -i 's/Version = "2.1.0"/Version = "2.2.0"/' internal/version/version.go
git commit -am "chore: bump 2.2.0"
./scripts/git-push.sh

# 2. 打 tag（触发 .github/workflows/release.yml）
git tag v2.2.0
git push https://github.com/losfer778/media_downloads.git v2.2.0
```

CI 会：交叉编译 5 个架构 → 打 fnOS fpk → 创建 GitHub Release 并上传全部附件 →
回写 `version.json` 与 `docs/RELEASES.md` → 同时构建并推送 ghcr 多架构镜像。

手写发布（不走 CI）：

```bash
VERSION=2.2.0 NOTES="本次更新内容" \
  ./scripts/build-release.sh --publish --commit
```

`--publish` 建 Release，`--commit` 把 `version.json` / `docs/RELEASES.md` 提交并推送，
保证「更新检测读到的摘要」和「Release 上的附件」一致。
