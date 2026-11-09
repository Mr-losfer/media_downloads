# 飞牛 fnOS（fpk）打包与安装

参考官方文档：<https://developer.fnnas.com/docs/guide/>

Media Super 的 fpk 模板在 [`deploy/fnos/`](../deploy/fnos)，打包脚本是
[`scripts/build-fpk.sh`](../scripts/build-fpk.sh)，一次生成 x86 与 arm 两个轻量包，
外加两个架构的 ffmpeg 运行时组件：

```bash
./scripts/build-fpk.sh
# build/mediasuper_2.5.5_fnos-x86.fpk        （约 3MB）
# build/mediasuper_2.5.5_fnos-arm.fpk        （约 3MB）
# build/ffmpeg-linux-amd64.gz                 （运行时组件，随 Release 发布）
# build/ffmpeg-linux-arm64.gz                 （运行时组件，随 Release 发布）
```

脚本会自动：

1. 检测本机有没有 `fnpack`，没有就从 `static2.fnnas.com` 下载对应架构的 1.2.3 版本到 `build/tools/`；
2. 交叉编译 `linux/amd64` 与 `linux/arm64`（`SKIP_BUILD=1` 可复用 `dist/` 里的产物）；
3. 按官方结构组装，替换 `manifest` 里的版本号 / 平台 / 更新说明（更新内容只取首行做 changelog）；
4. 调用 `fnpack build` 生成**轻量 fpk**，并把静态 ffmpeg 压缩成 `ffmpeg-linux-<arch>.gz` 作为 Release 附件；
5. 解包自检：顶层 7 项齐全、9 个生命周期脚本存在且 0755 / `#!/bin/bash` / LF / `bash -n` 通过、
   `manifest.checksum == md5(app.tgz)`、wizard 与 config 的 JSON 合法、轻量包体积守卫（< 16MB）。

> 为什么 ffmpeg 不再打进 fpk：v2.5.3 曾把约 168MB 的静态 ffmpeg 塞进 `app.tgz`，
> fpk 从 3MB 涨到 72MB；fpk 是单个 gzip 流且 `app.tgz` 排在最前面，旧版 fnOS 手动安装
> 「上传超过一分钟会报错」，Web 上传被截断后，排在后面的 `cmd/main` 等生命周期脚本根本没落盘，
> 于是报「缺少生命周期脚本 /cmd/main，安装包可能不完整」。
> v2.5.5 起 fpk 回到 **3MB 级**（与 2.1 一致），ffmpeg 改为**首次启动后台自动补齐**：
> 从 Release 下载对应架构的 `ffmpeg-linux-<arch>.gz`，校验 SHA256 与 ELF 头后解压到
> `<数据目录>/bin/ffmpeg`，`cmd/main` 启动时优先把它加入 PATH 与 `MS_FFMPEG`，补齐后无需重启。
> 设置页会显示进度，失败可点「立即安装 / 重新安装」手动重试，全程只下一次、不重复排队。
> 确实需要「开箱即用、不联网」时才用 `BUNDLE_FFMPEG=1 ./scripts/build-fpk.sh` 打内置版
> （体积回到 70MB 级，旧版 fnOS 不推荐）。

---

## 一、包内结构

`fnpack` 会把 `app/` 压成 `app.tgz`，其余文件平铺到 fpk 根目录：

```
mediasuper/
├── manifest                 # 应用元数据（版本、端口、入口）
├── ICON.PNG                 # 64×64
├── ICON_256.PNG             # 256×256
├── config/
│   ├── privilege            # run-as=package + 专用用户
│   └── resource             # 共享目录 mediasuper/downloads
├── cmd/                     # 9 个生命周期脚本，必须全部可执行
│   ├── main                 # start / stop / status / restart + setport / setpass
│   ├── install_init         # 建数据目录
│   ├── install_callback     # 写入安装向导的密码与端口
│   ├── upgrade_init         # 升级前停服务
│   ├── upgrade_callback     # 补齐端口配置后启动
│   ├── uninstall_init       # 卸载前停服务
│   ├── uninstall_callback
│   ├── config_init
│   └── config_callback      # 应用设置里改端口 / 密码
├── wizard/                  # 向导表单（JSON）
│   ├── install              # 安装时：管理员密码 + 端口
│   ├── upgrade              # 升级时：确认端口
│   ├── config               # 应用设置「配置」：改密码 / 端口
│   └── uninstall            # 卸载时：数据保留提示
└── app/
    ├── server/mediasuper    # 二进制
    └── ui/
        ├── config           # 桌面入口（iframe，端口 ${wizard_port}）
        └── images/icon-64.png, icon-256.png
```

`app.tgz` 里**不含 ffmpeg**，ffmpeg 由运行时补齐到数据目录。
生命周期脚本通过 `$0` 自身目录自定位 `main`，不再依赖 `${TRIM_APPDEST}/cmd/main`——
`TRIM_APPDEST` 指向 `target`（即 `app.tgz` 解压内容），而 `cmd/` 其实在应用根目录，
按旧路径找会误判「安装包不完整」并中止安装。

`manifest` 关键字段（完整值见 [`deploy/fnos/manifest.tmpl`](../deploy/fnos/manifest.tmpl)）：

```ini
appname      = mediasuper
version      = 2.5.5
display_name = Media Super
platform     = x86          # 或 arm，每个包只装一种架构
source       = thirdparty
service_port = 9099
checkport    = false
ctl_stop     = true
desktop_uidir = ui
desktop_applaunchname = mediasuper.main
```

`service_port` 只是默认值，真实端口由安装向导决定；`checkport = false` 是因为端口
由应用自己校验（用户改过端口后，系统再按 9099 检查会误判）。

`app/ui/config` 决定桌面图标点开后的行为（`iframe` 会在飞牛窗口内打开，比新标签页体验好）：

```json
{
  ".url": {
    "mediasuper.main": {
      "title": "Media Super",
      "icon": "images/icon-{0}.png",
      "type": "iframe",
      "protocol": "http",
      "port": "${wizard_port}",
      "url": "/",
      "allUsers": true
    }
  }
}
```

---

## 二、安装后目录

飞牛会把应用装到 `/var/apps/mediasuper`，并建立软链：

| 路径 | 实际位置 | 存什么 |
|---|---|---|
| `target/` | `/vol{n}/@appcenter/mediasuper` | 程序本体（`server/mediasuper`、`ui/`、`cmd/`） |
| `var/` | `/vol{n}/@appdata/mediasuper` | **数据**：`data/mediasuper.db`、日志、`bin/ffmpeg`、一键更新的新版本 |
| `etc/` | `/vol{n}/@appconf/mediasuper` | 配置 |
| `share/` | 共享目录 | `mediasuper/downloads`，文件管理器里可见 |

生命周期脚本用 `TRIM_APPDEST` / `TRIM_PKGVAR` / `TRIM_PKGETC` / `TRIM_DATA_SHARE_PATHS`
这些环境变量定位目录，不写死路径。

端口与密码的持久化：

| 文件 | 内容 | 说明 |
|---|---|---|
| `etc/settings.conf` | `PORT=9099` | 安装向导 / 应用设置写入，启动时优先读取 |
| `etc/.resetpass` | 待生效的管理员密码 | `0600`，启动时通过 `mediasuper -reset-password` 落库后立即删除 |
| `etc/.initpass` | 首次安装密码（兜底） | 仅首次启动使用，落库后删除 |

端口优先级：`settings.conf` → `TRIM_SERVICE_PORT`（manifest 默认值）→ `9099`。

下载目录优先用共享目录 `mediasuper/downloads`；没有共享目录时退回
`/var/apps/mediasuper/var/downloads`。

---

## 三、安装

1. 到 [Releases](https://github.com/Mr-losfer/media_downloads/releases) 下载对应架构的 fpk
   （`x86` 对应 Intel/AMD，`arm` 对应 ARM 机型）；
2. 飞牛桌面 → 应用中心 → 右上角「手动安装」→ 选择 fpk；
3. 安装向导会依次询问两项配置：

   | 步骤 | 字段 | 说明 |
   |---|---|---|
   | 管理员账号 | 管理员密码 / 确认密码 | 至少 6 位；**留空则使用默认密码 `losfer`** |
   | 服务端口 | Web 端口 | 默认 `9099`，范围 `1024-65535`，安装时自动检查占用 |

4. 安装完成后从桌面图标或应用中心打开，地址形如 `http://<NAS-IP>:<你填的端口>`；
5. 首次启动会在后台自动补齐 ffmpeg 组件（约 60–70MB，视网络几十秒到几分钟），
   补齐前 m3u8 任务会明确提示等待，可在「设置 → 下载」查看进度；
6. 安装后仍可在应用中心 → Media Super →「配置」里改密码或换端口。

> 密码不会明文留在磁盘上：向导值先写到 `etc/.resetpass`（`0600`），首次启动时
> 通过 `mediasuper -reset-password` 写进数据库的 PBKDF2 哈希，随后立刻删除该文件。

### 安装后修改端口 / 密码

应用中心 → Media Super →「配置」：

- **新管理员密码**：留空表示不改；填了就校验长度与两次一致；
- **Web 端口**：留空表示不改；新端口会先做占用检查，可用才写配置。

提交后 `config_callback` 会先停服务、写配置、再启动。启动时会把真实端口回写到
`app/ui/config`，保证桌面图标指向新端口（兼容不解析 `${wizard_port}` 的旧版系统）。

也可以命令行改：

```bash
/var/apps/mediasuper/cmd/main setport 9098    # 校验并写入 settings.conf
/var/apps/mediasuper/cmd/main setpass 'newpass'
/var/apps/mediasuper/cmd/main restart
```

---

## 四、运行与排查

```bash
# 状态
/var/apps/mediasuper/cmd/main status; echo $?

# 手动启停
/var/apps/mediasuper/cmd/main stop
/var/apps/mediasuper/cmd/main start

# 日志
tail -f /var/apps/mediasuper/var/mediasuper.log
```

常见问题：

**安装时报「缺少生命周期脚本 /cmd/main，安装包可能不完整」**
这是 v2.3–v2.4 的已知问题：安装回调把 `cmd/main` 定位到 `${TRIM_APPDEST}/cmd/main`，
而该路径在多数机型上并不存在，于是脚本自己报错并 `exit 1` 中止安装；72MB 的内置 ffmpeg
大包又会加重上传截断。v2.5.5 起回调改用 `$0` 自身目录优先、多路径查找，找不到也会
降级直写配置、绝不中止安装，同时 fpk 回到 3MB 级。遇到此提示请直接换 **v2.5.5 及以上**的 fpk 重装。

**启动失败，日志里提示端口占用**
到应用中心 →「配置」换一个端口即可，不用重新打包；也可以先停掉占用该端口的应用。

**升级后忘记端口**
看 `/var/apps/mediasuper/etc/settings.conf` 里的 `PORT`，或执行
`/var/apps/mediasuper/cmd/main resolve-port`。

**m3u8 能下但合成不了 mp4**
说明 ffmpeg 还没就绪。v2.5.5 起首次启动会在后台自动补齐，到「设置 → 下载」看状态：

- `正在下载 / 正在安装`：稍等即可，完成后前端会提示「ffmpeg 组件已就绪」；
- `安装失败`：点「立即安装 / 重新安装」重试，并检查 NAS 能否访问 GitHub / gh-proxy；
- 一直 `unsupported`（非 x86 / arm64 平台）或想用系统自带 ffmpeg：把绝对路径填进「ffmpeg 路径」。

命令行确认组件是否落盘：

```bash
which ffmpeg
ls -l /var/apps/mediasuper/var/bin/ffmpeg
```

**下载目录不可见**
共享目录在安装时创建，若被手动删除，重新安装或在应用设置里恢复目录授权即可。

**合成 mp4 时报 `Permission denied`**
应用对该保存目录没有写入权限（常见于手动填写的 `/vol00/...` 共享目录），或同名文件是只读 / 属于其它用户。
到应用设置里恢复目录授权，或在「设置 → 存储」换成应用可写的目录。
v2.5.4 起会在下载前真实探测可写性并给出明确提示，成品改用「临时文件 + 原子替换」写入，
同名只读文件不再导致失败，也不会反复重下整部片子。

**升级后桌面图标没刷新**

```bash
sudo systemctl restart trim_app_center.service
```

---

## 五、升级

两种都行：

- **应用中心**：安装新版 fpk，`upgrade_init` 会先停服务，`upgrade_callback` 再启动，
  `var/` 下的数据原样保留（已补齐的 `bin/ffmpeg` 也会保留，不必重新下载）；
- **后台一键更新**：设置 → 系统更新，新二进制落在 `var/bin/`，重启后自动接管。

---

## 六、官方文档索引

| 文档 | 链接 |
|---|---|
| 开发指南 | <https://developer.fnnas.com/docs/guide/> |
| 应用框架 / 生命周期 | <https://developer.fnnas.com/docs/core-concepts/framework/> |
| manifest 字段 | <https://developer.fnnas.com/docs/core-concepts/manifest/> |
| 环境变量 | <https://developer.fnnas.com/docs/core-concepts/environment-variables/> |
| 安装向导 | <https://developer.fnnas.com/docs/core-concepts/wizard/> |
| 权限 | <https://developer.fnnas.com/docs/core-concepts/privilege/> |
| 资源声明 | <https://developer.fnnas.com/docs/core-concepts/resource/> |
| fnpack | <https://developer.fnnas.com/docs/cli/fnpack/> |
