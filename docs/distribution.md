# 分发

本文档介绍公开订阅接口、查询参数、地区策略与选节点算法。

## 工作流程

终端用户请求订阅时，Paily 主服务按以下顺序处理：

1. 根据 `tag` 参数选择对应测活视角的存活节点缓存；未指定时使用全局缓存。
2. 若提供 `filter_unlock`，按 `filter_mode` 指定的语义过滤节点。
3. 根据 `preset` 决定地区与节点选择策略。
4. 按 `max_distribute` 限制输出数量。
5. 对选中节点重新命名，并交由对应格式生成器输出。

整个过程读取内存缓存，不会为每次请求扫描完整数据库。缓存会在测活结果入库后按需重建。

## 订阅地址

```text
https://your-domain.example/clash
https://your-domain.example/singbox
https://your-domain.example/base64
```

| 地址 | 输出 | 默认下载文件名 |
| --- | --- | --- |
| `/clash` | Clash YAML | `Paily Connect.yaml` |
| `/singbox` | Sing-box JSON | `Paily Connect.json` |
| `/base64` | Base64 文本 | `Paily Connect.txt` |

以上地址无需认证。基础链接可在管理界面的仪表盘和格式配置页面复制。

## 查询参数

| 参数 | 可选值 | 说明 |
| --- | --- | --- |
| `preset` | `common`、`all`、`other` | 地区与节点选择策略。 |
| `tag` | 已配置的测活后端 tag | 仅使用该测活视角最近一次初筛成功的节点。 |
| `filter_unlock` | 以 `\|` 分隔的服务名 | 例如 `netflix\|disney`。 |
| `filter_mode` | `select`、`mark` | 指定流媒体筛选语义，应与 `filter_unlock` 一起显式传入。 |

示例：

```text
/clash?preset=common
/singbox?tag=hk&preset=all
/base64?filter_unlock=netflix|disney&filter_mode=select
/clash?filter_unlock=netflix|youtube&filter_mode=mark
```

实际使用的 preset 优先级如下：

```text
URL preset > 当前 tag 的 default_preset > 全局 default_preset > common
```

## 地区策略

### `common`

`common` 优先选择常用地区 `HK`、`TW`、`SG`、`US`、`JP`：

1. 尽量在有节点的常用地区之间平均分配配额。
2. 某常用地区的节点超过配额时，进行均匀随机抽样，避免每次请求都集中使用固定的高分节点。
3. 常用地区不足时，先从其他常用地区随机补足。
4. 仍不足时按评分从其他地区补足，最后使用 `UN` 未知地区。
5. 若没有任何常用地区节点，自动退化为 `all`。

### `all`

`all` 侧重地区分布：

1. 非 `UN` 地区按节点数量比例分配配额。
2. 节点数不超过配额时全部选取。
3. 节点数略多于配额时，优先选取评分较高的节点。
4. 节点数明显多于配额时，按评分加权随机抽样。
5. `UN` 节点仅在其他地区不足时补足。

加权随机使用 A-Res 抽样，权重为 `max(评分, 0.01)`。评分较低的节点概率较低，但不会被完全排除。

### `other`

`other` 排除 `HK`、`TW`、`SG`、`US`、`JP`，其余规则与 `all` 相同。

## 流媒体筛选

服务名不区分大小写。

| 模式 | 语义 |
| --- | --- |
| `select` | 节点必须解锁全部指定服务，即交集。 |
| `mark` | 节点至少解锁一个指定服务，即并集；节点名称会显示实际解锁的标记。 |

建议始终显式传入 `filter_mode`。若未传入，服务端不会自动应用 `select` 过滤。

## 输出数量与排序

`max_distribute` 决定单次订阅最多输出的节点数，默认 `200`。若存活节点总数不足该值，则全部输出。

输出结果按地区展示顺序排列，同一地区内按评分降序编号。节点名称由命名模板生成，包含解锁标记、等级、国旗、地区、序号和 sponsor text，详见 [配置与 API](configuration-and-api.md)。

当前实现下，若没有候选节点，接口仍可能返回 HTTP `200` 及空的格式内容。订阅客户端不应仅依据 HTTP 状态码判断订阅是否包含节点。
