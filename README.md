# Git Sync Tool

一个用于同步 Git 仓库对的 Web 工具，支持定时同步和 GitHub Actions 自动生成。

## 功能特性

- ✅ Web 界面管理 Repo Pair 配置
- ✅ 设置源仓库和目标仓库的 Token
- ✅ 定时自动同步（支持间隔时间设置）
- ✅ 手动触发同步
- ✅ 自动生成 GitHub Actions 工作流
- ✅ 自动构建并推送 Docker 镜像到 Docker Hub
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
   - **Source Repo**: 源仓库（格式：owner/repo）
   - **Source Token**: 源仓库访问 Token
   - **Target Repo**: 目标仓库（格式：owner/repo）
   - **Target Token**: 目标仓库访问 Token
   - **Sync Schedule**: 同步间隔（如 `1h`, `30m`, `3600s`），留空则仅手动同步
   - **Enable GitHub Actions**: 是否自动生成 CI/CD 工作流
   - **Docker Image**: Docker 镜像名称
   - **Docker Hub User/Pass**: Docker Hub 凭证

3. 点击 **Save** 保存配置
4. 点击 **Sync Now** 立即触发同步

## 生成的 GitHub Actions

启用 GitHub Actions 后，工具会在目标仓库中创建 `.github/workflows/docker-build.yml`：

```yaml
name: Build and Push Docker Image

on:
  push:
    branches: [ main, master ]
  pull_request:
    branches: [ main, master ]

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
    - uses: actions/checkout@v3
    
    - name: Set up Docker Buildx
      uses: docker/setup-buildx-action@v2
    
    - name: Login to Docker Hub
      uses: docker/login-action@v2
      with:
        username: <your-dockerhub-user>
        password: ${{ secrets.DOCKERHUB_TOKEN }}
    
    - name: Build and push
      uses: docker/build-push-action@v4
      with:
        context: .
        push: true
        tags: <dockerhub-user>/<image-name>:latest
```

### 配置 Docker Hub Secret

在目标 GitHub 仓库中设置 Secret：
1. 进入仓库 → Settings → Secrets and variables → Actions
2. 添加 New repository secret
3. Name: `DOCKERHUB_TOKEN`
4. Value: 你的 Docker Hub Access Token（不是密码）

## API 接口

### GET /api/pairs
获取所有 Repo Pair 配置

### POST /api/pairs
添加新的 Repo Pair

### PUT /api/pairs/{id}
更新指定 Repo Pair

### DELETE /api/pairs/{id}
删除指定 Repo Pair

### POST /api/pairs/{id}/sync
手动触发同步

### GET /api/pairs/{id}/status
获取同步状态

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
      "enable_actions": true,
      "docker_image": "my-app",
      "docker_hub_user": "myuser",
      "docker_hub_pass": "xxx"
    }
  ]
}
```

## 安全提示

⚠️ **重要：**
- 妥善保管 `config.json` 文件，包含敏感 Token
- 不要将配置文件提交到版本控制
- 建议使用 Docker Hub Access Token 而非密码
- 生产环境请使用 HTTPS

## 依赖

- Go 1.21+
- github.com/go-git/go-git/v5
- github.com/gorilla/mux
- github.com/google/go-github/v57
- golang.org/x/oauth2

## License

MIT
