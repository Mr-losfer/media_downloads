# 部署：Docker 单容器与 Compose

镜像：`ghcr.io/mr-losfer/media-super`（公开，支持 `linux/amd64` / `linux/arm64` / `linux/arm/v7`）

- 最新版：`latest`
- 固定版本：`:2.1.0`
- 也可以从[仓库](https://github.com/Mr-losfer/media_downloads)源码自行构建

> 默认端口 **9099**（容器内 8080），默认密码 **losfer**，首次登录后请到「设置 → 管理员密码」修改。

---

## 一、单容器（docker run）

```bash
mkdir -p /vol1/1000/mediasuper/data
mkdir -p /vol1/1000/影视

docker run -d \
  --name mediasuper \
  --restart unless-stopped \
  -p 9099:8080 \
  -e TZ=Asia/Shanghai \
  -e MS_ADMIN_PASS=losfer \
  -e MS_SAVE_DIRS="/downloads:影视" \
  -e MS_MAX_TASKS=2 \
  -e MS_SEG_CONNS=8 \
  -v /vol1/1000/mediasuper/data:/data \
  -v /vol1/1000/影视:/downloads \
  --memory 512m \
  --cpus 1 \
  ghcr.io/mr-losfer/media-super:latest
```

打开 `http://<NAS-IP>:9099`。

参数说明：

| 参数 | 作用 |
|---|---|
| `-p 9099:8080` | 左边是宿主机端口，可改成任意空闲端口 |
| `-v .../data:/data` | **必须**。数据库、临时分片、一键更新的新版本都在这里 |
| `-v .../影视:/downloads` | 下载落盘目录，与 `MS_SAVE_DIRS` 的路径对应 |
| `--memory 512m` | 低配保护，可按机器调整 |
| `-e MS_ADMIN_PASS` | 只在**首次启动**生效，之后密码存在数据库里 |

需要多个下载目录时：

```bash
-e MS_SAVE_DIRS="/downloads/电影:电影,/downloads/剧集:剧集" \
-e MS_DEFAULT_DIR=/downloads/电影 \
-v /vol1/电影:/downloads/电影 \
-v /vol2/剧集:/downloads/剧集
```

---

## 二、Docker Compose（推荐）

仓库根目录已带一份可直接用的 `docker-compose.yml`：

```bash
# 1. 取 compose 文件（也可以 git clone 整个仓库）
curl -fsSLO https://raw.githubusercontent.com/Mr-losfer/media_downloads/main/docker-compose.yml

# 2. 起服务
mkdir -p data downloads
docker compose up -d

# 3. 看日志（首次启动会打印 API Key）
docker compose logs -f

# 4. 打开 http://<NAS-IP>:9099 ，密码 losfer
```

自定义下载目录与端口，两种写法任选。

**写法 A：环境变量**

```bash
DOWNLOAD_DIR=/vol1/1000/影视 docker compose up -d
```

**写法 B：直接改 compose**

```yaml
services:
  mediasuper:
    image: ghcr.io/mr-losfer/media-super:latest
    ports:
      - "9099:8080"          # 改成 8099:8080 就是换端口
    volumes:
      - ./data:/data
      - /vol1/1000/影视:/downloads
```

常用命令：

```bash
docker compose ps
docker compose restart
docker compose pull && docker compose up -d   # 升级镜像
docker compose down                            # 停止（数据保留在 ./data）
```

---

## 三、从源码构建（改代码 / 没有外网）

```bash
git clone https://gh-proxy.com/https://github.com/Mr-losfer/media_downloads.git
cd media_downloads

# 方式 1：compose 构建
docker compose -f deploy/docker/docker-compose.yml up -d --build

# 方式 2：先编译再打镜像（机器内存小时更稳）
make cross
docker build -f deploy/docker/Dockerfile.bin \
  --build-arg BIN=dist/mediasuper-linux-amd64 \
  -t mediasuper:local .
```

`make cross` 会产出 `dist/mediasuper-linux-{amd64,arm64,armv7,armv6,386}`。

---

## 四、目录与权限

容器以 **uid/gid 1000** 运行（镜像内置 `app` 用户）：

```bash
# 宿主机目录属主不对时，下载会失败
chown -R 1000:1000 /vol1/1000/影视 /vol1/1000/mediasuper/data
```

如果你宿主机用户不是 1000，可以显式指定：

```bash
docker run ... --user $(id -u):$(id -g) ...
```

此时需要保证 `/data` 对该用户可写。

---

## 五、反向代理 / HTTPS

Nginx 示例（注意 SSE 必须关闭缓冲，否则任务进度不动）：

```nginx
location / {
    proxy_pass http://127.0.0.1:9099;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # SSE（/api/events）
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 3600s;
}
```

用了域名后，建议在「设置 → 对外访问地址」填上 `https://media.example.com`，
这样 API 文档页里的示例链接会直接使用公网地址。

---

## 六、升级

```bash
# 换镜像
docker compose pull && docker compose up -d

# 或者后台一键更新（设置 → 系统更新），容器重启后依然生效
```

两种方式的区别见 [UPDATE.md](UPDATE.md)。

---

## 七、常见问题

**拉不动 ghcr.io？**
国内网络访问 ghcr 经常超时，可以：

1. 用代理/加速器拉取；
2. 或本地构建：`docker compose -f deploy/docker/docker-compose.yml up -d --build`；
3. 或直接下载 Release 里的裸二进制，用 `Dockerfile.bin` 打镜像。

**容器起来了但页面 502 / 一直转？**
先看 `docker compose logs --tail=100`。常见原因是 `/data` 不可写，程序无法打开数据库。

**下载失败提示目录不在允许列表？**
任务只允许写到「设置 → 保存目录」里列出的路径。新挂载的目录要先去设置页加进去。

**内存占用多少？**
空载约 15–25MB，两个任务并发约 30–40MB。`mem_limit 512m` 足够。
