# Paily Connect Core

> [!WARNING]
> **该代码库非直接生产可用**
>
> Paily Connect 系列是我们的内部项目，其仍处于早期开发阶段，目前该公开版本部分功能仍存在较多问题。
>
> **我们极度不建议您直接部署此版本**。该版本可能会有潜在的未知问题并可能造成数据丢失等异常。
>
> Paily Connect 系列开源仓库在近期尚无继续推进计划，我们建议您等待可用的分支版本部署。

本仓库是 Paily Connect 开源代码仓库。Paily Connect 是一个免费订阅项目，它从互联网上的订阅源和节点源收集节点，经过解析、去重和测活后，根据节点的各种表现综合评分，最后按地区和评分筛选，生成 Clash、Sing-box 或 Base64 订阅。

平台由三个协作的服务组成：

| 服务 | 仓库 | 职责 |
| --- | --- | --- |
| **Paily 主服务** | [paily-core](https://github.com/openpaily/paily-core) | 维护源库与节点池，负责去重、存活判定、评分、分发和管理 API。 |
| **Paily Fetch** | [paily-fetch](https://github.com/openpaily/paily-fetch) | 从源库获取来源，下载订阅、解析内容、校验节点，并上报结果。 |
| **Paily 测活后端** | [paily-check](https://github.com/openpaily/paily-check) | 从节点池获取节点，执行初筛与复筛，并上报检测结果。 |

本仓库是 **Paily 主服务**（`paily-core`），也是平台的中枢。各服务之间通过 HTTP API 通信，统一使用 `service_secret` 完成服务间认证。

Paily 主服务另配有网页前端，请访问 [paily-frontend](https://github.com/openpaily/paily-frontend) 以获取详情。

## 工作流程

平台的完整数据流转分为四个阶段。

1. **提交来源。** Paicatch 或管理员通过管理界面，把订阅源（`subscribe`）或节点（`node`）提交到源库。源库按类型和内容去重。

2. **抓取与入库。** Paily Fetch 定期向 Paily 主服务获取启用中的来源，下载、识别、解析并校验节点，然后上报。Paily 主服务合并重复节点，写入节点池，并保留节点与来源的关联关系。

3. **测活与评分。** Paily 测活后端从节点池拉取节点，先做初筛判断是否存活，再对存活节点做复筛，测量详细延迟、抖动、出口地区、流媒体解锁情况和可选下载速度，最后把结果上报。Paily 主服务据此更新节点的存活状态，并根据节点的各种表现综合评分。

4. **筛选与分发。** 每次测活结果入库后，Paily 主服务构建一份分发缓存。终端用户请求 `/clash`、`/singbox` 或 `/base64` 时，主服务按地区比例和评分筛选节点，重新命名后生成对应格式的订阅。

```text
  订阅源 / 节点
        │
        ▼
   Paily Fetch        下载 · 识别 · 解析 · 校验
        │
        ▼
   Paily 主服务  ◄────── 管理界面 / Paicatch
  源库 · 节点池 · 去重 · 历史
        │
        ▼
   Paily 测活后端        初筛 · 复筛（延迟 · 地区 · 流媒体 · 速度）
        │
        ▼
   Paily 主服务         存活判定 · 评分 · 缓存 · 筛选
        │
        ▼
 /clash  /singbox  /base64
```

## 主要能力

- 维护源库，区分订阅源和节点，按类型和内容去重，并根据历史自动裁掉长期失效的来源。
- 维护节点池，按协议、地址、端口和密码去重，保留节点与多个来源的对应关系。
- 接收 Paily 测活后端的初筛与复筛结果，判断节点存活，并根据节点的各种表现综合评分。
- 支持按地区、存活状态、协议、来源、评分、流媒体解锁能力和自定义表达式筛选节点。
- 支持使用赞助商名称与表达式命名模板，自动生成节点名称。
- 公开分发 Clash、Sing-box、Base64 三种订阅格式，支持按检测视角、地区策略和流媒体能力分发。

## 启动

> [!IMPORTANT]
> 目前对于 Sqlite 的支持尚不完整，**请使用 PostgreSQL 部署**。
> 我们建议您将数据库部署在配备 NVMe 存储、至少 2 核 4G 的服务器上。当数据量较大时，数据库的存储容量和读写压力都会显著增加。

需要 Go 1.24，以及可用的 Paily Fetch 和 Paily 测活后端。

复制示例配置并按需修改：

```bash
cp config.example.yaml config.yaml
```

完整配置项及说明见 [`config.example.yaml`](config.example.yaml)。

构建并启动：

```bash
go build -o paily-core ./cmd/server
./paily-core -config ./config.yaml
```

## 管理界面

Paily Connect 配套提供 Web 管理界面，用于日常管理：

- **仪表盘**：查看节点与来源统计，复制基础订阅链接。
- **来源管理**：创建、编辑、删除来源，查看抓取记录与关联节点。
- **节点管理**：筛选节点，查看原始配置、来源和测活历史。
- **Sponsor、筛选引擎预览、运行时配置和格式配置**。
- **日志**：查看抓取日志与初筛检测日志。

## 开发

接口采用 spec-first 流程：`api/openapi/spec.yaml` 是 API 契约的唯一来源，服务端代码由 `oapi-codegen` 生成。

安装与生成文件版本一致的工具：

```bash
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.6.0
```

修改 `api/openapi/spec.yaml` 后，重新生成服务端接口：

```bash
oapi-codegen -config oapi-codegen.yaml api/openapi/spec.yaml
```

生成配置见 `oapi-codegen.yaml`，输出到 `internal/api/generated/server.gen.go`，其中包含 Gin 路由注册、请求模型和 `ServerInterface`。当前生成文件由 `v2.6.0` 生成，建议使用相同版本，避免产生无关差异。

开发约定：

- 不要手动修改 `internal/api/generated/` 下的生成文件。
- 业务实现在 `internal/api/handler/`，实现生成的 `ServerInterface`。
- 变更接口时先修改 `spec.yaml`，再运行生成命令，最后补齐 handler 实现并通过 `go build ./...`。

## 文档

| 文档 | 内容 |
| --- | --- |
| [源库与来源管理](docs/source-management.md) | 订阅源与节点、源库去重、死源判定、管理界面操作。 |
| [节点池](docs/node-pool.md) | 节点去重、节点与来源的关联、存活判定。 |
| [评分机制](docs/scoring.md) | Paily 主服务如何根据节点表现计算评分。 |
| [分发](docs/distribution.md) | 订阅地址、查询参数、地区策略与选节点算法。 |
| [订阅模板](docs/templates.md) | 分发时的订阅模板配置与制作方法。 |
| [配置与 API](docs/configuration-and-api.md) | 启动配置、动态参数、筛选引擎、命名与 Sponsor、API 权限。 |

## 许可证

本项目基于 GNU Affero General Public License v3.0（AGPL-3.0）发布，详见 [LICENSE](LICENSE)。