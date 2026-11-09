# 部署指南

本文覆盖 ReadTrace 的常见部署方式：Linux 二进制 + systemd、Windows 开机自启、Docker / Docker Compose、1Panel，以及反向代理、升级回滚、备份迁移和故障排查。

只需要运行程序时，**不需要安装 Go**；只有从源码构建才需要 Go 1.27+。程序是单个静态二进制，前端已经内嵌。

---

## 1. 选择部署方式

| 场景 | 推荐方式 | 数据位置 |
| --- | --- | --- |
| Linux 服务器长期运行 | 二进制 + systemd | `/opt/readtrace/data` |
| Docker / NAS / 群晖 / Unraid | Docker Compose | 命名卷或宿主目录 |
| 1Panel | Go 运行环境，运行预编译二进制 | 运行目录下的 `data/` |
| Windows 个人电脑 | exe + 快捷方式 / 任务计划 | exe 同级的 `data/` |
| 本地开发 | `go run ./cmd/readtrace` | 项目目录或 `-data` 指定 |
| 公网访问 | 反向代理 + HTTPS | 同上 |

无论哪种方式，都需要注意：

- 数据目录必须持久化，里面包含 SQLite、实例锁和加密主密钥。
- 容器内运行时，程序必须监听 `0.0.0.0`，否则宿主机端口映射访问不到。
- 公网访问必须加 HTTPS，否则管理密码和会话会以明文传输。

---

## 2. 准备二进制

### 2.1 确认服务器架构

```bash
uname -m
```

| 输出 | 使用文件 |
| --- | --- |
| `x86_64` | `linux_amd64` |
| `aarch64` / `arm64` | `linux_arm64` |
| Apple Silicon Mac | `macos_arm64` |
| Intel Mac | `macos_amd64` |
| Windows x64 | `windows_amd64.exe` |
| Windows ARM64 | `windows_arm64.exe` |

`macos` 对应 Go 内部的 `darwin` 目标，不是另一个操作系统。

### 2.2 从 Release 下载

Release 使用固定文件名，不带版本号：

```text
readtrace_linux_amd64
readtrace_linux_arm64
readtrace_macos_amd64
readtrace_macos_arm64
readtrace_windows_amd64.exe
readtrace_windows_arm64.exe
```

版本由 Release 标签和 `readtrace -version` 提供。固定文件名可以配合 `/releases/latest/download/` 使用，服务器更新脚本不用每次改版本号：

```bash
curl -fLO https://github.com/xiaoxura/ReadTrace/releases/latest/download/readtrace_linux_amd64
chmod +x readtrace_linux_amd64
```

### 2.3 从源码构建

需要 Go 1.27+：

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=0.2.1" -o readtrace ./cmd/readtrace
```

Linux 交叉编译：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w -X main.version=0.2.1" -o readtrace ./cmd/readtrace
```

仓库的 GitHub Actions 发布流程也会为六个平台构建单文件可执行程序，见 [`.github/workflows/release.yml`](../.github/workflows/release.yml)。

---

## 3. 数据目录与权限

默认数据目录是**可执行文件同级的 `data/`**，也可以用 `-data` 或 `READTRACE_DATA` 指定。

```text
readtrace(.exe)
data/
├── readtrace.db      # SQLite：账号、运行记录、统计、日志、定时任务、推送渠道、设置
├── readtrace.db-wal  # SQLite WAL（运行时可能存在）
├── readtrace.db-shm  # SQLite 共享内存（运行时可能存在）
├── readtrace.lock    # 数据目录实例锁；退出后文件可能保留，锁随进程释放
└── secret.key        # 本地 AES-256-GCM 主密钥（32 字节）
```

注意：

- `secret.key` 丢失后，已保存的账号 Cookie 和推送 Token 无法解密，需要重新登录账号。
- 同一数据目录只能由一个实例使用；不能通过换端口绕过实例锁。
- 数据库和密钥文件权限为 `0600`，数据目录建议 `0700`。
- 运行程序的用户必须对数据目录有写权限。
- 备份和迁移时把整个 `data/` 目录一起带走，不要只复制 `readtrace.db`。

默认监听 `127.0.0.1:8720`，只适合本机或反向代理场景。容器里要让外部访问，必须显式监听 `0.0.0.0:8720`：

```bash
./readtrace -addr 0.0.0.0:8720 -data ./data
```

---

## 4. Linux 二进制 + systemd

### 4.1 安装

```bash
# 创建专用用户（已存在则跳过）
sudo useradd --system --home /opt/readtrace --shell /usr/sbin/nologin readtrace

sudo install -d -m 755 -o readtrace -g readtrace /opt/readtrace
sudo install -d -m 700 -o readtrace -g readtrace /opt/readtrace/data
sudo install -m 755 -o readtrace -g readtrace ./readtrace /opt/readtrace/readtrace
```

### 4.2 systemd 服务

创建 `/etc/systemd/system/readtrace.service`：

```ini
[Unit]
Description=ReadTrace
After=network.target

[Service]
Type=simple
User=readtrace
Group=readtrace
WorkingDirectory=/opt/readtrace
ExecStart=/opt/readtrace/readtrace -addr 127.0.0.1:8720 -data /opt/readtrace/data -no-open
Restart=always
RestartSec=3

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/readtrace/data

[Install]
WantedBy=multi-user.target
```

仓库也提供了示例文件：[`deploy/readtrace.service`](../deploy/readtrace.service)。

启用并启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now readtrace
sudo systemctl status readtrace
journalctl -u readtrace -f
```

### 4.3 直接手动运行

临时调试可以直接运行：

```bash
./readtrace -addr 127.0.0.1:8720 -no-open -data ./data
```

需要局域网访问时改为 `-addr 0.0.0.0:8720`，并配置防火墙。

---

## 5. Windows 开机自启

把 `readtrace.exe` 放到固定目录，例如 `C:\ReadTrace`。首次运行后会在同级生成 `data` 目录。

### 5.1 快捷方式

创建快捷方式放到 `shell:startup`：

```text
目标： "C:\ReadTrace\readtrace.exe"
起始位置： C:\ReadTrace
```

### 5.2 任务计划程序

- 触发器：登录时
- 操作：启动 `C:\ReadTrace\readtrace.exe`
- 参数：`-no-open`（如果希望启动后自动打开浏览器，可以去掉）
- 起始位置：`C:\ReadTrace`

数据默认在 `C:\ReadTrace\data`，也可以用 `-data D:\ReadTraceData` 放到其他磁盘。

---

## 6. Docker

### 6.1 构建并运行

```bash
docker build -t readtrace .
docker volume create readtrace-data

docker run -d --name readtrace \
  --restart unless-stopped \
  -p 127.0.0.1:8720:8720 \
  -v readtrace-data:/opt/readtrace/data \
  readtrace
```

如果要直接通过局域网访问，把端口改成 `-p 8720:8720`，并确保防火墙已放行。

查看日志：

```bash
docker logs -f readtrace
```

### 6.2 Docker Compose

创建 `compose.yaml`：

```yaml
services:
  readtrace:
    build: .
    image: readtrace:local
    container_name: readtrace
    restart: unless-stopped
    ports:
      - "127.0.0.1:8720:8720"
    volumes:
      - readtrace-data:/opt/readtrace/data
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8720/api/health"]
      interval: 30s
      timeout: 3s
      retries: 3

volumes:
  readtrace-data:
```

启动：

```bash
docker compose up -d --build
docker compose logs -f
```

### 6.3 使用宿主机目录

命名卷会继承镜像中数据目录的所有权。若改用宿主机目录，需要给容器用户 UID/GID `10001:10001` 写权限：

```bash
sudo install -d -m 700 -o 10001 -g 10001 /srv/readtrace/data

docker run -d --name readtrace \
  -p 127.0.0.1:8720:8720 \
  -v /srv/readtrace/data:/opt/readtrace/data \
  readtrace
```

不要用一个普通的 `0755` 空目录直接替换数据目录，否则容器内非 root 用户可能无法写入。

---

## 7. 1Panel

1Panel 的 Go 运行环境本质上是 `golang` 容器，工作目录是 `/app`，宿主运行目录会挂载到 `/app`：

- 宿主 `/opt/ReadTrace/readtrace` ↔ 容器 `/app/readtrace`
- 宿主 `/opt/ReadTrace/data` ↔ 容器 `/app/data`

### 7.1 准备文件

```bash
uname -m   # x86_64 用 linux_amd64，aarch64/arm64 用 linux_arm64
mkdir -p /opt/ReadTrace
# 从 Release 下载对应二进制，重命名为 readtrace 后放到 /opt/ReadTrace/
chmod +x /opt/ReadTrace/readtrace
```

### 7.2 创建 Go 运行环境

| 字段 | 填写 |
| --- | --- |
| 名称 | `ReadTrace` |
| 应用 | 选 Go 1.27；面板只有 Go 1.26 也可以，但必须运行预编译二进制，不能 `go run` |
| 运行目录 | `/opt/ReadTrace` |
| 启动命令 | `exec ./readtrace -addr 0.0.0.0:8720` |
| 容器名称 | `ReadTrace` |
| 端口 | 容器端口 `8720` |
| 主机映射 | 宿主 `8720` → 容器 `8720`，宿主端口被占用时换成其他端口 |
| 挂载 | 不需要。1Panel 已把宿主运行目录挂载到容器 `/app` |
| 环境变量 | 可选 `TZ=Asia/Shanghai`；用域名访问时可加 `READTRACE_HOST_ALLOW=你的域名` |

「端口外部访问」打开。

### 7.3 关键说明

- 启动命令写 `exec ./readtrace -addr 0.0.0.0:8720`。`exec` 让进程直接接管容器，停止/重启时信号能正确传递。
- 必须显式监听 `0.0.0.0`。程序默认只监听 `127.0.0.1`，这是为了本地直接运行时的安全；容器内只监听回环地址时，1Panel 的「端口外部访问」即使打开，宿主端口映射也到不了容器里的服务。「端口外部访问」只负责宿主机端口和防火墙，不改变进程监听地址。
- 不需要 `-data`，也不需要额外挂载：数据默认在可执行文件同级的 `data/`，即容器内 `/app/data`，对应宿主 `/opt/ReadTrace/data`。
- `-no-open` 可以省略：容器内没有 `xdg-open`，启动浏览器失败会被忽略，不影响服务。
- 项目 `go.mod` 要求 Go 1.27+。如果走源码编译，必须选 1.27；选 1.26 会报 `go.mod requires go >= 1.27.0`。上传预编译二进制则不依赖 Go 版本。

启动命令和「环境变量」二选一即可：

```text
# 方式一：写在启动命令里（推荐）
exec ./readtrace -addr 0.0.0.0:8720

# 方式二：启动命令保持 exec ./readtrace
READTRACE_ADDR=0.0.0.0:8720
```

### 7.4 用源码运行（可选）

如果 1Panel 的 Go 版本是 1.27+，也可以把整个仓库放进运行目录：

```bash
go run ./cmd/readtrace -addr 0.0.0.0:8720 -data /app/data
```

这里必须显式传 `-data /app/data`，因为 `go run` 的临时可执行文件不在项目目录，默认数据目录会跑到临时目录，无法持久化。

### 7.5 更新

在 1Panel 停止容器，替换宿主 `/opt/ReadTrace/readtrace`，再启动容器。`/opt/ReadTrace/data/` 不要删。

---

## 8. 其他面板 / NAS

群晖、威联通、Unraid、宝塔等面板本质上都是 Docker，按第 6 节处理：

- 镜像：用仓库 `Dockerfile` 本地构建，或在面板里导入构建好的镜像。
- 端口：宿主 `8720` → 容器 `8720`。
- 数据：挂载到容器 `/opt/readtrace/data`。
- 架构：NAS 常见 `linux/amd64` 或 `linux/arm64`，选对应镜像/二进制。
- 容器内程序已经由 Dockerfile 设为 `0.0.0.0:8720`，不需要额外改监听地址。

如果面板提供的是类似 1Panel 的 Go 运行环境，则参考第 7 节，注意容器内路径通常是 `/app`。

---

## 9. 反向代理与 HTTPS

生产环境建议让程序只监听 `127.0.0.1`，由 Nginx/Caddy 负责 HTTPS。

### 9.1 Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name readtrace.example.com;

    ssl_certificate     /etc/letsencrypt/live/readtrace.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/readtrace.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8720;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Real-IP $remote_addr;

        # SSE 实时面板：关闭缓冲并放宽超时
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
    }
}
```

### 9.2 Caddy

```caddyfile
readtrace.example.com {
    reverse_proxy 127.0.0.1:8720
}
```

Caddy 会自动申请证书并设置 `X-Forwarded-Proto`。

### 9.3 Host 白名单

用域名访问时，建议启用 Host 白名单防止 DNS 重绑定：

```bash
./readtrace -addr 127.0.0.1:8720 -host-allow readtrace.example.com
```

或环境变量：

```bash
READTRACE_HOST_ALLOW=readtrace.example.com
```

多个域名用逗号分隔。未设置时不校验 Host；如果设置了但访问域名不匹配，会返回 `403`。

反向代理必须传递原始 `Host`，并设置 `X-Forwarded-Proto: https`，否则 HTTPS 下会话 Cookie 可能不会带 `Secure`。

---

## 10. 升级与回滚

### 10.1 二进制 / systemd

```bash
sudo systemctl stop readtrace
sudo install -m 755 -o readtrace -g readtrace ./readtrace /opt/readtrace/readtrace
sudo systemctl start readtrace
```

数据目录保持不变。替换前建议先备份 `data/`。

### 10.2 Docker Compose

```bash
docker compose up -d --build
```

如果使用固定镜像，则拉取新镜像后 `docker compose up -d`。

### 10.3 1Panel

停止运行环境 → 替换 `/opt/ReadTrace/readtrace` → 启动。数据在 `data/`，不需要动。

### 10.4 回滚

程序回滚前先确认数据库结构是否兼容。数据库迁移是向前执行的，用旧版本程序打开新版本数据库可能不兼容；需要回滚时，先恢复对应版本的数据备份，再替换二进制。

---

## 11. 备份与迁移

### 11.1 备份

建议先停止服务，确保 SQLite WAL 已落盘：

```bash
sudo systemctl stop readtrace
sudo tar -czf readtrace-data-$(date +%F).tar.gz -C /opt/readtrace data
sudo systemctl start readtrace
```

至少保留 `readtrace.db` 和 `secret.key`，但推荐整个 `data/` 一起备份。

### 11.2 迁移到新机器

1. 在新机器安装同版本或更新版本的程序。
2. 停止新旧两边的服务。
3. 把整个 `data/` 目录复制过去。
4. 修正目录和文件所有权/权限。
5. 启动新实例，确认账号、运行记录、定时任务和推送渠道都在。

```bash
sudo install -d -m 700 -o readtrace -g readtrace /opt/readtrace/data
sudo rsync -a /path/to/backup/data/ /opt/readtrace/data/
```

`secret.key` 必须一起迁移，否则加密数据无法解密。

### 11.3 保留策略

运行详情和日志默认保留 **90 天**，可用 `-retain-days` 调整，`0` 表示永久保留。超期详情会被清理，但最小统计事实继续保留，用于累计和趋势。

---

## 12. GitHub 与自动部署

GitHub 不是这个项目的运行环境，不能直接用 GitHub Pages 托管，因为服务需要长期运行、SQLite 持久化、SSE 和对外请求。

当前 GitHub 的作用是：

- 托管源码；
- GitHub Actions 在推送 `v*` 标签后构建六个平台的单文件可执行程序；
- 生成 Release 和 `SHA256SUMS`。

如果希望“发版后自动更新服务器”，可以额外加一个 GitHub Actions 部署工作流，通过 SSH 登录服务器：

1. 下载固定文件名的 Release 二进制；
2. 替换 `/opt/readtrace/readtrace` 或 `/opt/ReadTrace/readtrace`；
3. 重启 systemd 服务或 1Panel 容器。

需要配置 `DEPLOY_HOST`、`DEPLOY_USER`、`DEPLOY_SSH_KEY` 等 Secrets。SSH 私钥不要写入仓库，建议使用专用部署用户并限制权限。

---

## 13. 故障排查

| 现象 | 排查 |
| --- | --- |
| `listen tcp ... address already in use` | 端口被占用，`ss -lntp \| grep 8720` 查进程，或换端口 |
| 容器端口映射后访问不到 | 程序是否监听 `0.0.0.0`，而不是 `127.0.0.1` |
| 1Panel 外部访问打开仍打不开 | 检查启动命令里的 `-addr 0.0.0.0:8720`，以及容器端口/宿主端口 |
| 反向代理 502 | 程序是否启动、代理是否指向 `127.0.0.1:8720` |
| 页面能开但数据不实时 | 代理是否关闭 buffering，SSE 超时是否足够 |
| `403 拒绝访问：Host 不在白名单中` | `-host-allow` 是否包含当前访问域名 |
| `permission denied` 写数据库 | 数据目录所有权/权限，容器用户 UID 是否为 `10001` |
| `go.mod requires go >= 1.27.0` | 源码构建改用 Go 1.27+，或直接运行预编译二进制 |
| 容器重建后数据没了 | `data/` 是否挂载/持久化到宿主目录 |
| 忘记面板密码 | 停止服务，执行 `./readtrace -reset-password -data /path/to/data`，再启动 |

---

## 14. 参考

- [README](../README.md)
- [Dockerfile](../Dockerfile)
- [systemd 示例](../deploy/readtrace.service)
- [GitHub Release 工作流](../.github/workflows/release.yml)
