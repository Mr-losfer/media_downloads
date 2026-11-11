# FnDepot 第三方商店上架说明

[FnDepot](https://github.com/EWEDLCM/FnDepot) 是飞牛 fnOS 的第三方应用商店，采用**外部应用源**模式：源由作者自行发布，用户在客户端里添加源地址后即可看到并安装应用，FnDepot 不托管安装包。本仓库按 [V2 编写说明](https://github.com/EWEDLCM/FnDepot#fndepot-%E5%A4%96%E9%83%A8%E5%BA%94%E7%94%A8%E6%BA%90-v2-%E7%BC%96%E5%86%99%E8%AF%B4%E6%98%8E) 上架 **Media Super**。

> 上架属于元数据发布：**不修改应用版本号、不打 tag、不发 Release**，直接复用已发布的 `v2.5.7` FPK 附件，因此已安装用户不会收到应用内更新提示。

## 一、源地址

| 方式 | 地址 |
| --- | --- |
| GitHub 仓库地址（推荐） | `https://github.com/Mr-losfer/media_downloads` |
| JSON 直链 | `https://raw.githubusercontent.com/Mr-losfer/media_downloads/main/fnpack.json` |

在 FnDepot 客户端（版本需 `> 0.0.7`，且已适配 fnOS httponly 认证）选择「添加外部源」，填入以上任一地址即可。客户端优先读取仓库默认分支根目录下**文件名完全匹配**的 `fnpack.json`。

## 二、目录结构

```
media_downloads/
├── fnpack.json                      # V2 源索引（根目录，文件名/大小写固定）
├── assets/mediasuper/
│   ├── ICON.PNG                     # 商店图标 256×256（56KB，规范建议 < 500KB）
│   ├── ICON_512.PNG                 # 高清备用 512×512
│   ├── Preview/1.png ~ 5.png        # 商店预览图 1280×720，单张 < 2MB
│   ├── README.md                    # 详情页 README 分页
│   └── RELEASE_NOTES.md             # 更新内容
├── deploy/fnos/                     # fpk 源码（图标已同步为同一张图）
├── web/                             # Web 管理台（PWA 图标已同步）
└── docs/FNDEPOT.md                  # 本文
```

`icon_url`、`preview_urls`、`readme_url` 均使用相对路径，相对于 `fnpack.json` 所在地址解析；`download_url` 使用绝对 URL 指向 GitHub Release 附件。

## 三、应用字段

| 字段 | 值 | 说明 |
| --- | --- | --- |
| `apps` 键名 | `mediasuper` | 必须与 FPK manifest 的 `appname` 完全一致（区分大小写） |
| `display_name` | `Media Super` | 商店显示名 |
| `platform` | `["x86", "arm"]` | 只允许 `all` / `x86` / `arm` |
| `categories` | `["影音娱乐"]` | 只能用九个固定分类，最多两个 |
| `run_as` | `package` | 与 `deploy/fnos/config/privilege` 的 `run-as: package` 一致 |
| `install_type` | `""` | 空字符串 = 存储空间（`root` 才是系统空间） |
| `is_docker` | `false` | 布尔值，非 Docker 应用 |
| `service_port` | `9099` | 默认端口，安装向导可改 |
| `os_min_version` | `0.9.0` | 与 FPK manifest 一致 |
| `maintainer` | `losfer` | 不使用 `FnDepot` 作为开发者/发布者/源作者名称 |

`source_info.author` 为 `Mr-losfer`，表示该源由项目作者维护；`maintainer` 才是应用开发者，两者职责不同。

## 四、版本与安装包

客户端先按设备架构与系统版本过滤，再选择**最高可用版本**；安装包选择顺序为 `packages.[当前架构] → packages.all`。本源为 x86 / arm 分别提供：

| 架构 | 文件 | size (Bytes) | SHA256 |
| --- | --- | --- | --- |
| x86 | `mediasuper_2.5.7_fnos-x86.fpk` | `3375601` | `5708a250384d3acdc9bf9721461d292b0bea259f08a408ffb303b3b8973d29f5` |
| arm | `mediasuper_2.5.7_fnos-arm.fpk` | `3112083` | `afb5d0f0bc659c929201753b4772929e69087daf70a516bcac021d42d358d59c` |

两个下载地址经 `https://gh-proxy.com/` 代理前缀指向 `https://github.com/Mr-losfer/media_downloads/releases/download/v2.5.7/…`（与应用内置「优先 gh-proxy、异常直连」的更新策略一致，避免国内 NAS 直连 GitHub Release 失败）。`sha256` / `size` 与 `version.json` 中的 `fpk` 映射一致。存在 `sha256` 时客户端**强制校验**，因此 `size` 与哈希必须与已发布文件逐字节一致；代理只是转发，不改变文件内容。

### 发布前检查命令

```bash
jq empty fnpack.json                                   # 严格 JSON
sha256sum mediasuper_2.5.7_fnos-{x86,arm}.fpk
stat -c '%n %s' mediasuper_2.5.7_fnos-{x86,arm}.fpk
# 核对 FPK 内 manifest 的 appname / version / platform
```

也可以用 Python 直接比对 `fnpack.json` 与 `version.json`：

```bash
python3 -c "import json; d=json.load(open('fnpack.json')); v=json.load(open('version.json')); \
p=d['apps']['mediasuper']['releases']['2.5.7']['packages']; \
print(all(p[a]['sha256']==v['fpk'][f['name']]['sha256'] and p[a]['size']==v['fpk'][f['name']]['size'] \
for a,f in [('x86',{'name':'mediasuper_2.5.7_fnos-x86.fpk'}),('arm',{'name':'mediasuper_2.5.7_fnos-arm.fpk'})]))"
```

### 发布新版本（重要）

1. 在 `releases` 下**新增**一个可比较的版本键（如 `2.5.8`），不要静默替换同版本 FPK。
2. 更新该版本的 `updated_at`、两段 `download_url`、`size`、`sha256`。
3. 已发布的「版本号 + 架构」文件视为不可变；内容变更必须发新版本或新地址。
4. 图标、预览图、README 可随源更新，不需要发应用版本。

## 五、收录与可见性

FnDepot 每天自动运行 `scripts/generate_sources.py`：搜索含 `fndepot` 的仓库以及代码搜索 `filename:fnpack.json`，校验源结构、必填字段、死亡仓库（6 个月未更新）与重复源后，把合格源写入根目录 `valid_sources.txt`。

为让本仓库能被稳定发现，仓库已添加 `fndepot` topic，并在 `fnpack.json` 中保持至少一个合规应用。客户端还会按「应用名 + 版本」指纹做重复源判定，因此**不要再用本项目的同一应用去 fork 一份 FnDepot 仓库**，否则后添加的重复源会被自动停用。

## 六、下载地址与代理

`download_url` 默认带代理前缀，实测在本环境**直连 GitHub Release 不可达（超时）、代理返回 200 且内容哈希与已发布 FPK 完全一致**：

```
https://gh-proxy.com/https://github.com/Mr-losfer/media_downloads/releases/download/v2.5.7/mediasuper_2.5.7_fnos-x86.fpk
```

若你的网络可直连 GitHub、或代理服务不可用，去掉 `https://gh-proxy.com/` 前缀即可，内容不变、SHA256 仍可通过校验：

```
https://github.com/Mr-losfer/media_downloads/releases/download/v2.5.7/mediasuper_2.5.7_fnos-x86.fpk
```

改完后重新执行上面的校验命令即可，不需要重新打包 FPK，也不需要发布新版本。

## 七、常见问题

- **看不到应用**：确认客户端版本 `> 0.0.7`、源地址可访问、`fnpack.json` 在仓库根目录且大小写正确；单个应用字段错误只会跳过该应用，不影响同源其他应用。
- **架构不可安装**：确认 `platform` 与 `packages` 的键只包含 `all` / `x86` / `arm`，且对应 `download_url` 实际可访问。
- **SHA256 校验失败**：`size` 单位必须是 Bytes 且等于文件实际字节数，不能写 `3.2 MB`。
- **切系统语言**：`fnpack.json` 已提供 `zh-CN` 基础字段与 `en-US` 的 `i18n`，回退顺序由客户端按 `TRIM_SYS_LANGUAGE` 处理。
