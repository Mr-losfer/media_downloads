# 飞牛 fnOS（fpk）打包与安装

参考官方文档：<https://developer.fnnas.com/docs/guide/>

Media Super 的 fpk 模板在 [`deploy/fnos/`](../deploy/fnos)，打包脚本是
[`scripts/build-fpk.sh`](../scripts/build-fpk.sh)，一次生成 x86 与 arm 两个包：

```bash
./scripts/build-fpk.sh
# build/mediasuper_2.2.0_fnos-x86.fpk
# build/mediasuper_2.2.0_fnos-arm.fpk
```

脚本会自动：

1. 检测本机有没有 `fnpack`，没有就从 `static2.fnnas.com` 下载对应架构的 1.2.3 版本到 `build/tools/`；
2. 交叉编译 `linux/amd64` 与 `linux/arm64`（`SKIP_BUILD=1` 可复用 `dist/` 里的产物）；
3. 按官方结构组装，替换 `manifest` 里的版本号 / 平台 / 更新说明；
4. 调用 `fnpack build` 生成 fpk。

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
├── cmd/                     # 生命周期脚本
│   ├── main                 # start / stop / status / restart
│   ├── install_init         # 建数据目录
│   ├── install_callback
│   ├── upgrade_init         # 升级前停服务
│   ├── upgrade_callback     # 升级后启动
│   ├── uninstall_init       # 卸载前停服务
│   ├── uninstall_callback
│   ├── config_init
│   └── config_callback
└── app/
    ├── server/mediasuper    # 二进制
    └── ui/
        ├── config           # 桌面入口（iframe，端口 9099）
        └── images/icon-64.png, icon-256.png
```

`manifest` 关键字段（完整值见 [`deploy/fnos/manifest.tmpl`](../deploy/fnos/manifest.tmpl)）：

```ini
appname      = mediasuper
version      = 2.2.0
display_name = Media Super
platform     = x86          # 或 arm，每个包只装一种架构
source       = thirdparty
service_port = 9099
ctl_stop     = true
desktop_uidir = ui
desktop_applaunchname = mediasuper.main
```

`app/ui/config` 决定桌面图标点开后的行为（`iframe` 会在飞牛窗口内打开，比新标签页体验好）：

```json
{
  ".url": {
    "mediasuper.main": {
      "title": "Media Super",
      "icon": "images/icon-{0}.png",
      "type": "iframe",
      "protocol": "http",
      "port": "9099",
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
| `target/` | `/vol{n}/@appcenter/mediasuper` | 程序本体（`server/mediasuper`、`ui/`） |
| `var/` | `/vol{n}/@appdata/mediasuper` | **数据**：`data/mediasuper.db`、日志、一键更新的新版本 |
| `etc/` | `/vol{n}/@appconf/mediasuper` | 配置 |
| `share/` | 共享目录 | `mediasuper/downloads`，文件管理器里可见 |

生命周期脚本用 `TRIM_APPDEST` / `TRIM_PKGVAR` / `TRIM_SERVICE_PORT` / `TRIM_DATA_SHARE_PATHS`
这些环境变量定位目录，不写死路径。

下载目录优先用共享目录 `mediasuper/downloads`；没有共享目录时退回
`/var/apps/mediasuper/var/downloads`。

---

## 三、安装

1. 到 [Releases](https://github.com/losfer778/media_downloads/releases) 下载对应架构的 fpk
   （`x86` 对应 Intel/AMD，`arm` 对应 ARM 机型）；
2. 飞牛桌面 → 应用中心 → 右上角「手动安装」→ 选择 fpk；
3. 安装完成后从桌面图标或应用中心打开，地址形如 `http://<NAS-IP>:9099`；
4. 默认密码 `losfer`，登录后到「设置 → 管理员密码」修改。

端口 9099 被占用时，改 `manifest` 的 `service_port` 与 `app/ui/config` 的 `port` 后重新打包。

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

**启动失败，日志里提示端口占用**
换个端口重新打包，或先在应用中心停掉占用 9099 的应用。

**m3u8 能下但合成不了 mp4**
系统里没有 `ffmpeg`。飞牛自带 ffmpeg 的机型会自动命中 `/usr/bin/ffmpeg` 或 `/usr/trim/bin/ffmpeg`；
如果确实没有，可以：

```bash
# 确认路径
which ffmpeg
# 在「设置 → ffmpeg 路径」里填绝对路径
```

**下载目录不可见**
共享目录在安装时创建，若被手动删除，重新安装或在应用设置里恢复目录授权即可。

**升级后桌面图标没刷新**

```bash
sudo systemctl restart trim_app_center.service
```

---

## 五、升级

两种都行：

- **应用中心**：安装新版 fpk，`upgrade_init` 会先停服务，`upgrade_callback` 再启动，
  `var/` 下的数据原样保留；
- **后台一键更新**：设置 → 系统更新，新二进制落在 `var/bin/`，重启后自动接管。

---

## 六、官方文档索引

| 文档 | 链接 |
|---|---|
| 开发指南 | <https://developer.fnnas.com/docs/guide/> |
| 应用框架 / 生命周期 | <https://developer.fnnas.com/docs/core-concepts/framework/> |
| manifest 字段 | <https://developer.fnnas.com/docs/core-concepts/manifest/> |
| 环境变量 | <https://developer.fnnas.com/docs/core-concepts/environment-variables/> |
| 权限 | <https://developer.fnnas.com/docs/core-concepts/privilege/> |
| 资源声明 | <https://developer.fnnas.com/docs/core-concepts/resource/> |
| fnpack | <https://developer.fnnas.com/docs/cli/fnpack/> |
