# 配置与 API

本文档介绍 Paily 主服务的配置层次、可调参数、筛选引擎、节点命名与 sponsor text，以及接口权限划分。

## 配置层次

Paily 主服务的配置分为两层：

1. **启动配置**：通过 YAML 文件或环境变量提供，涵盖监听端口、数据库、认证和测活后端列表等，需重启生效。
2. **动态配置**：保存在数据库中，涵盖评分、分发、历史保留和命名等参数，可在管理界面修改，无需重启。

启动配置的优先级为：

```text
环境变量 > YAML 配置文件 > 内置默认值
```

环境变量统一以 `PAILY_` 为前缀，嵌套键中的 `.` 替换为 `_`。例如 `server.port` 对应 `PAILY_SERVER_PORT`。

## 启动配置

| YAML 键 | 环境变量 | 说明 |
| --- | --- | --- |
| `server.port` | `PAILY_SERVER_PORT` | HTTP 监听端口，内置默认值 `8080`。 |
| `server.cors_origins` | `PAILY_SERVER_CORS_ORIGINS` | 精确允许的浏览器 Origin；`*` 会被忽略。 |
| `server.verbose` | `PAILY_SERVER_VERBOSE` | 是否记录全部请求。 |
| `database.type` | `PAILY_DATABASE_TYPE` | `sqlite` 或 `postgres`。 |
| `database.dsn` | `PAILY_DATABASE_DSN` | SQLite 文件路径或 PostgreSQL DSN。 |
| `auth.admin_password` | `PAILY_AUTH_ADMIN_PASSWORD` | 管理员密码或 bcrypt 哈希。 |
| `auth.service_secret` | `PAILY_AUTH_SERVICE_SECRET` | Paily Fetch、Paily 测活后端和源提交器使用的服务密钥。 |
| `auth.jwt_secret` | `PAILY_AUTH_JWT_SECRET` | JWT HMAC 密钥。 |
| `checkers[].tag` | 配置文件 | 测活后端 tag。 |
| `checkers[].secret` | 配置文件 | 该测活后端专用密钥。 |
| `checkers[].default_preset` | 配置文件 | 该 tag 订阅未指定 preset 时的默认预设。 |
| `default_preset` | `PAILY_DEFAULT_PRESET` | 全局默认预设。 |

## 动态配置

动态配置可在管理界面的「运行时配置」页面修改，首次启动时会写入缺失的键。

| 分类 | 键 | 默认值 | 说明 |
| --- | --- | ---: | --- |
| 分发 | `max_distribute` | `200` | 单次订阅最多输出的节点数。 |
| 分发 | `node_distribute_filter` | 空 | 命中表达式的节点不进入分发缓存。 |
| 存活 | `node_alive_check_window_minutes` | `240` | 最新初筛的存活窗口，单位分钟。 |
| 评分 | `score_decay_alpha` | `0.7` | 评分的时间衰减系数。 |
| 评分 | `score_weight_latency` | `0.7` | 延迟权重。 |
| 评分 | `score_weight_stability` | `0.3` | 稳定性权重。 |
| 评分 | `score_weight_speed` | `0.55` | 有测速数据时的速度混合权重。 |
| 评分 | `score_latency_curve_k` | `0.3` | 延迟曲线参数。 |
| 评分 | `score_jitter_linear_threshold_ms` | `200` | 抖动分段阈值，单位毫秒。 |
| 命名 | `grade_a_min` | `0.75` | A 等级下限。 |
| 命名 | `grade_b_min` | `0.50` | B 等级下限。 |
| 命名 | `grade_c_min` | `0.25` | C 等级下限，低于此值为 D。 |
| 历史 | `fetch_dead_window_days` | `7` | 抓取失效判定窗口，单位天。 |
| 历史 | `node_dead_window_days` | `4` | 节点测活失效判定窗口，单位天。 |
| 历史 | `fetch_history_window_days` | `7` | 抓取历史基础保留天数。 |
| 历史 | `check_run_history_days` | `7` | 测活历史基础保留天数。 |
| 历史 | `cleanup_interval_minutes` | `30` | 清理间隔，单位分钟。 |
| 文件名 | `filename_clash` | `Paily Connect.yaml` | Clash 下载文件名。 |
| 文件名 | `filename_singbox` | `Paily Connect.json` | Sing-box 下载文件名。 |
| 文件名 | `filename_base64` | `Paily Connect.txt` | Base64 下载文件名。 |

`dead_latency_ms` 会写入默认配置，但当前不参与存活判定；不可达以初筛的延迟 `-1` 表示。

## 筛选引擎

筛选引擎基于 [expr-lang](https://expr-lang.org/)，用于管理端的节点与来源搜索、表达式预览、分发缓存过滤和 sponsor text 匹配。布尔表达式应返回 `true` 或 `false`。

```text
# 排除未知地区或低评分节点
Region == "UN" || Score < 0.35

# 匹配 VLESS 且评分较高的节点
Protocol == "vless" && Score >= 0.7
```

节点表达式中可读取以下字段：

```text
Server, Protocol, Password, Hash, Region, Alive, Score,
SourceIDs, Sources, RawConfig, Streaming
```

来源表达式中可读取以下字段：

```text
ID, Identifier, Info, Status, Type
```

## 节点命名与 Sponsor

`node_name_template` 是返回字符串的表达式模板，默认输出形如：

```text
[解锁服务] 等级 - 国旗 地区 序号 Sponsor 文案
```

序号按地区内评分排序后单独编号，例如 `hk01`、`hk02`、`tw01`。解锁标记仅在指定了流媒体筛选且满足显示条件时出现。

Sponsor 依据 `priority` 从小到大依次匹配，后匹配项覆盖前项，因此数值更大的 priority 最终获胜。没有任何匹配时使用 `default_sponsor_text`。

## 格式配置

管理界面的「格式配置」页面可管理 `clash`、`singbox`、`base64` 各自的 JSON 配置。Paily 主服务将这些内容视为由格式生成器定义的不透明 JSON，修改前建议读取并备份当前配置。格式生成能力来自 Paily Fetch 的格式模块；Clash 与 Sing-box 的模板编写方法见 [订阅模板](templates.md)。

## API 与权限

| 身份 | 凭据 | 可访问范围 |
| --- | --- | --- |
| 终端用户 | 无 | `/health`、`/clash`、`/singbox`、`/base64`、登录和刷新。 |
| 管理员 | access JWT | 全部管理 API。 |
| Paily Fetch / 源提交器 | `service_secret` | `/api/v1/fetch/*` 与 `POST /api/v1/sources`。 |
| Paily 测活后端 | 专用测活密钥或 `service_secret` | `/api/v1/check/*`。专用密钥对应配置的 tag；`service_secret` 使用 `default` tag。 |

`POST /api/v1/sources` 同时允许管理员 JWT 与 `service_secret`，用于管理界面手工建源和源提交器自动提交。其他普通管理资源不接受 `service_secret`。

完整接口定义见 [`../api/openapi/spec.yaml`](../api/openapi/spec.yaml)。
