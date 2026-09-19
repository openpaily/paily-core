# 订阅模板

Paily 主服务在首次启动时，会为每个已注册的订阅格式写入一份默认格式配置（见 `internal/templates/` 下的 `clash.yaml` 与 `singbox.json`）。管理员可在管理界面的「格式配置」页面修改。

当格式配置模板为空时，生成器只输出一个包含节点列表的极简文档。模板的具体处理规则由 Paily Fetch 的格式生成器实现，Clash 与 Sing-box 的规则不同。

## Clash 模板

Clash 模板必须是一份完整的 Clash/Mihomo YAML 配置。生成时：

1. 顶层的 `proxies` 列表会被替换为本次选中的全部节点。
2. 每个 `proxy-groups` 条目的 `proxies` 列表会追加本次全部节点的名称。

因此模板应保留完整的 `proxy-groups`、`rules` 等结构，而 `proxies` 处可以留空，生成时会被替换。

```yaml
mixed-port: 7890
allow-lan: true
mode: rule
proxies: []            # 会被替换为选中节点
proxy-groups:
  - name: 选择
    type: select
    proxies: []        # 会被追加全部节点名称
rules:
  - MATCH,选择
```

## Sing-box 模板

Sing-box 模板必须是一份完整的 sing-box JSON 配置。生成时：

1. 模板中的管理 outbound 会被保留：`selector`、`urltest`、`direct`、`dns`、`block`、`tproxy`、`redirect`、`tun`、`sniffer`。
2. 模板中原有的代理 outbound 会被丢弃，替换为本次选中的节点。
3. 每个 `selector` 与 `urltest` 的 `outbounds` 列表会追加本次全部节点的 tag。
4. 最终 `outbounds` 顺序为：`selector` / `urltest` + 代理节点 + `direct` 等基础设施。
5. Sing-box 不支持的协议会被跳过。

```json
{
  "outbounds": [
    { "type": "selector", "tag": "select", "outbounds": [] },
    { "type": "direct", "tag": "direct" }
  ]
}
```

## 制作步骤

1. 先在客户端验证一份完整可用的 Clash 或 sing-box 配置。
2. 记下其中的分组名称（Clash 的 `proxy-groups`，Sing-box 的 `selector` / `urltest`）和出站规则；`proxies` 或代理 outbound 可以留空，生成时会被替换。
3. 在「格式配置」页面选择对应格式，把配置全文填入 `template` 字段并保存。
4. 访问公开订阅地址验证输出结果。

内置模板只在格式配置为空时写入，不会覆盖已经自定义过的配置。如果需要恢复内置模板，可清空该格式的 `template` 后重启服务。