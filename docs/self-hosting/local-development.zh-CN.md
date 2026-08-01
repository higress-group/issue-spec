# 本地开发 issue-spec Server

**[English](local-development.md) | 简体中文**

本指南用于从当前源码检出构建 `issue-spec-server`，并连接本地 PostgreSQL。这里的
fixture 只适用于开发和试用；生产安装请遵循[部署与加固指南](operations/deployment.md)。

## 前置条件

- [`go.mod`](../../go.mod) 声明的 Go 工具链；
- 带 Compose 插件的 Docker；
- `make`、`openssl` 和 `curl`。

普通 Go 构建不需要 Node.js，因为仓库已经提交 Web 应用产物，Server 会把它内嵌到
二进制中。只有修改 Web 应用并重新生成资源时，才需要安装
[`web/package.json`](../../web/package.json) 声明的包管理器。

使用下面任一路径前，先从仓库根目录生成仅供开发使用的密钥文件：

```bash
mkdir -p deployments/dev/secrets
umask 077
openssl rand -hex 32 > deployments/dev/secrets/bootstrap
openssl rand -hex 32 > deployments/dev/secrets/token-pepper
openssl rand -hex 32 > deployments/dev/secrets/encryption-key
```

这些文件已被 Git 忽略。不要在生产环境复用，也不要把内容打印到日志中。

## 使用 Compose 运行完整环境

这是试用浏览器工作台的最短路径：

```bash
docker compose -f deployments/dev/compose.yaml --profile server up -d --build --wait
curl -fsS http://127.0.0.1:8080/readyz
```

打开 <http://127.0.0.1:8080/bootstrap>，输入
`deployments/dev/secrets/bootstrap` 中的 Bootstrap Secret，创建首位本地管理员。
这条开发 Bootstrap 路径不要求配置外部身份 Provider。

默认端口被占用时，应同时修改 Server 暴露端口和公开 URL：

```bash
ISSUE_SPEC_POSTGRES_PORT=55432 \
ISSUE_SPEC_SERVER_PORT=18080 \
ISSUE_SPEC_PUBLIC_URL=http://127.0.0.1:18080 \
docker compose -f deployments/dev/compose.yaml --profile server up -d --build --wait
curl -fsS http://127.0.0.1:18080/readyz
```

`ISSUE_SPEC_POSTGRES_PORT` 只修改 PostgreSQL 的宿主机端口。容器内 Server 仍通过
Compose 网络的 5432 端口连接 PostgreSQL。

## 在宿主机上构建 Server

只启动 PostgreSQL：

```bash
docker compose -f deployments/dev/compose.yaml up -d --wait postgres
```

使用仓库中已提交的 Web 资源构建 Server 二进制：

```bash
make build-server
```

配置宿主机进程并启动：

```bash
export ENVIRONMENT=development
export LISTEN_ADDR=127.0.0.1:8080
export DATABASE_URL='postgres://issue_spec:issue_spec@127.0.0.1:5432/issue_spec?sslmode=disable'
export API_PUBLIC_URL=http://127.0.0.1:8080
export WEB_PUBLIC_URL=http://127.0.0.1:8080
export BOOTSTRAP_SECRET_FILE="$PWD/deployments/dev/secrets/bootstrap"
export TOKEN_PEPPER_FILE="$PWD/deployments/dev/secrets/token-pepper"
export ENCRYPTION_KEY_FILE="$PWD/deployments/dev/secrets/encryption-key"
export MIGRATIONS_MODE=auto
export SEARCH_MODE=disabled
./dist/issue-spec-server
```

在另一个终端中确认就绪，再打开浏览器：

```bash
curl -fsS http://127.0.0.1:8080/readyz
```

如果要把 PostgreSQL 发布到其他宿主机端口，应在启动 fixture 前设置端口，并在
`DATABASE_URL` 中使用相同端口：

```bash
ISSUE_SPEC_POSTGRES_PORT=55432 \
docker compose -f deployments/dev/compose.yaml up -d --wait postgres
export DATABASE_URL='postgres://issue_spec:issue_spec@127.0.0.1:55432/issue_spec?sslmode=disable'
```

`MIGRATIONS_MODE=auto` 是默认值，会持有 PostgreSQL Advisory Lock 并应用内嵌 Schema，
重复启动是安全的。`SEARCH_MODE=disabled` 也是默认值，因为标准
`postgres:17-alpine` fixture 不包含 `pg_bigm` 或 `pg_jieba`；启用前请阅读
[可选 PostgreSQL Issue 检索](operations/deployment.md#optional-postgresql-issue-search)。

## 使用 PostgreSQL 运行 Server 测试

设置 `TEST_DATABASE_URL` 后，测试会创建并删除隔离的 Schema：

```bash
export TEST_DATABASE_URL='postgres://issue_spec:issue_spec@127.0.0.1:5432/issue_spec?sslmode=disable'
make test-server
```

使用非默认 `ISSUE_SPEC_POSTGRES_PORT` 时，也要同步修改 `TEST_DATABASE_URL`。未设置该
变量时，需要 PostgreSQL 的测试会自动跳过。

## 选择构建制品

- 在源码检出中开发时使用 `make build-server`。
- Go 原生试用可以使用
  `go install github.com/higress-group/issue-spec/cmd/issue-spec-server@latest`；需要可重复
  构建时应固定到不可变 Commit 或语义化版本。
- 使用 `make docker-server IMAGE=registry.example/issue-spec-server:VERSION` 为运维方自己的
  Registry 构建加固后的 Runtime 镜像。生产部署和回滚应优先使用不可变镜像 Digest。

无论选择哪种 Server 制品，PostgreSQL 都应保持为独立、由运维方管理的服务，不要打进
Server 镜像。

## 停止或重置 Fixture

停止容器但保留 PostgreSQL 数据卷：

```bash
docker compose -f deployments/dev/compose.yaml --profile server down
```

如果确定要删除全部本地 fixture 数据，可以增加 `--volumes`；该操作无法撤销。
