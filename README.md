# Git Sync Tool

一个用于双向同步 Git 仓库对的 Web 工具，支持定时同步和详细的同步日志。

## 功能特性

- ✅ Web 界面管理 Repo Pair 配置
- ✅ 设置源仓库和目标仓库的 Token
- ✅ 定时自动同步（支持间隔时间设置）
- ✅ 手动触发同步
- ✅ 双向同步，不会覆盖任何仓库内容
- ✅ 详细的同步日志记录
- ✅ 导出和导入配置
- ✅ 实时查看同步状态

## 快速开始

### 1. 构建并运行

```bash
cd git-sync-tool
go mod tidy
go run main.go
```

服务将在 `http://localhost:8080` 启动。

### 2. 使用 Web 界面

1. 打开浏览器访问 `http://localhost:8080`
2. 填写 Repo Pair 信息：
   - **Name**: 配对名称
   - **Source Repo**: 源仓库（格式：owner/repo 或完整 URL）
   - **Source Token**: 源仓库访问 Token
   - **Target Repo**: 目标仓库（格式：owner/repo 或完整 URL）
   - **Target Token**: 目标仓库访问 Token
   - **Sync Schedule**: 同步间隔（如 `1h`, `30m`, `3600s`），留空则仅手动同步

3. 点击 **Save** 保存配置
4. 点击 **Sync Now** 立即触发同步
5. 点击 **Logs** 查看同步日志
6. 点击 **Export Config** 导出配置
7. 点击 **Import Config** 导入配置

## API 接口

### GET /api/pairs
获取所有 Repo Pair 配置

### POST /api/pairs
添加新的 Repo Pair

### GET /api/pairs/{id}
获取指定 Repo Pair 的详细信息

### PUT /api/pairs/{id}
更新指定 Repo Pair

### DELETE /api/pairs/{id}
删除指定 Repo Pair

### POST /api/pairs/{id}/sync
手动触发同步

### GET /api/pairs/{id}/status
获取同步状态

### GET /api/config
获取完整的配置

### POST /api/config
导入配置

## 配置文件

配置保存在 `config.json`，格式如下：

```json
{
  "repo_pairs": [
    {
      "id": "1234567890",
      "name": "My Project",
      "source_repo": "owner/source-repo",
      "source_token": "ghp_xxx",
      "target_repo": "owner/target-repo",
      "target_token": "ghp_yyy",
      "schedule": "1h",
      "last_sync": "2024-01-01T12:00:00Z",
      "status": "success",
      "logs": [
        "2024-01-01 12:00:00: 开始同步",
        "2024-01-01 12:00:01: 源仓库克隆成功",
        "2024-01-01 12:00:02: 目标仓库推送成功",
        "2024-01-01 12:00:03: 源仓库推送成功",
        "2024-01-01 12:00:03: 双向同步完成: 2024-01-01T12:00:03Z"
      ]
    }
  ]
}
```

## 安全提示

⚠️ **重要：**
- 妥善保管 `config.json` 文件，包含敏感 Token
- 不要将配置文件提交到版本控制
- 生产环境请使用 HTTPS

## 依赖

- Go 1.21+
- github.com/go-git/go-git/v5
- github.com/go-git/go-git/v5/config
- github.com/gorilla/mux

## Docker 部署

### 构建 Docker 镜像

```bash
docker build -t git-sync-tool .
```

### 运行 Docker 容器

```bash
docker run -d -p 8080:8080 --name git-sync-tool git-sync-tool
```

### Docker Stack 部署

使用 `docker-compose.yml` 文件进行 Docker Stack 部署：

```bash
docker stack deploy -c docker-compose.yml git-sync
```

## License

MIT
