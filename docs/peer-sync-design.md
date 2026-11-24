# 节点同步改造设计：对等双向同步

- 状态：已按本稿实现（v26.15 开发中，测试机三面板联调通过）
- 日期：2026-11-24
- 涉及模块：`web/service/node_sync.go`、`web/controller/node_sync.go`、`web/html/xui/node_sync.html`、`database/model/*`、入站/出站/路由三类数据的写入口

## 1. 背景

当前实现是主/副面板单向推送：主面板全量覆盖副面板的入站/出站/路由。主面板故障期间只能在副面板上改数据，主面板恢复后（自动同步最快 5 分钟内）会把副面板的修改覆盖掉，数据无声丢失。

改为对等模型：去掉主副概念，任何面板都可以和已添加的对端双向同步；以"上次同步基线"做三方合并，冲突按条目时间戳判定，后改的覆盖。

## 2. 已确认的决策

| 决策点 | 结论 |
| --- | --- |
| 角色模型 | 对等，无主副 |
| 判定机制 | 每对面板保存同步基线，三方合并（基线 / 本地 / 对端） |
| 多台 | 每台维护有序对端列表，每轮按顺序串行逐个同步；链式逐跳传播 |
| 冲突 | 同一 tag 两边改成不同内容：后改的覆盖；时间相同用 panelId 字典序定胜负（两端计算结果一致） |
| 删除 | 删除参与合并，墓碑带时间戳，删除对撞编辑同样"后改的胜" |
| 时间戳用途 | 只用于冲突裁决和墓碑，不用它决定整表归属 |

非目标：

- 不同步流量计数（按邮箱保留本地计数，与现状一致）。
- 不同步面板访问配置（端口 / WebBasePath / 证书 / 登录账号）。
- 不做客户端级合并（同一入站内两边各改不同客户端，v1 以整个入站为最小合并单位）。
- 不再提供"整库接管"的初始化；需要整库迁移时用数据库导入功能。

## 3. 数据模型

### 3.1 `sync_nodes` 扩展

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `Sort` | int | 对端顺序（同步顺序，也是界面展示顺序） |
| `PeerId` | string | 对端 panelId，添加对端时从对端 `info` 获取 |
| `BaseData` | text | 上次同步后的基线快照 JSON（dataset + state）；列表查询时显式 Select 排除该列 |

角色字段 `settings.syncRole` / `settings.syncMasterName` 废弃：升级时清空。同步与否只看"有没有对端"。

### 3.2 新表 `sync_item_state`

```sql
CREATE TABLE sync_item_state (
  kind       TEXT    NOT NULL,           -- inbound | outbound | rule
  key        TEXT    NOT NULL,           -- 条目 tag
  changed_at INTEGER NOT NULL,           -- 本面板时钟，毫秒
  deleted    INTEGER NOT NULL DEFAULT 0, -- 墓碑标记
  PRIMARY KEY (kind, key)
);
```

- 面板本地维护，随同步数据集体交换，用于冲突裁决。
- 升级迁移：现存入站/出站/路由全部 upsert `changed_at = 0`（视为最旧），首轮同步不产生误判。
- 墓碑永久保留（按 tag 一行，体积可忽略）；后续需要再加清理任务。

### 3.3 面板身份

新增 `settings.panelUUID`（首次使用时生成）：

- 添加到对端时记录对方的 panelId 为 `PeerId`，用于校验 `receive-merge` 来源；
- 冲突时间完全相同时按 panelId 字典序破平局，保证两端算出同一结果。

## 4. 条目时间戳打点

原则：只有影响同步数据的内容变化才打点；流量计数写入绝不打点。

不使用 GORM 钩子（`AddTraffic` 每隔几十秒就 `UPDATE inbounds SET up/down`，钩子会误触发），改为在 service 层显式调用 `syncState.Touch(kind, tag, deleted)`。

| 写入口 | 打点 |
| --- | --- |
| `InboundService.AddInbound` / `UpdateInbound` | `inbound, tag` |
| `InboundService.DelInbound` | `inbound, tag, deleted` |
| `InboundService.AddInboundClient` / `UpdateInboundClient` / `DelInboundClient` | `inbound, 所属入站 tag` |
| `InboundService.DelDepletedClients` | `inbound, 每个受影响 tag` |
| `OutboundService.AddOutbound` / `UpdateOutbound` | `outbound, tag` |
| `OutboundService.SetFirstOutbound` | `outbound, 所有受影响 tag`（首条即默认出站，顺序即语义） |
| `OutboundService.DelOutbound` | `outbound, tag, deleted` |
| `RoutingRuleService.SaveAllRules` | 与当前库 diff：新增 / 内容或 Sort 变化 / 删除，逐 tag 打点 |
| `InboundService.autoRenewClients`（后台） | `inbound, 受影响 tag` |
| `InboundService.disableInvalidInbounds`（后台） | `inbound, 受影响 tag` |
| `InboundService.disableInvalidClients` | 只动 `client_traffics`，不打点 |

注意：后台自动任务也时间戳，意味着"一侧自动续费"晚于"另一侧人工修改"时会覆盖后者，属"后写者胜"的必然结果，见第 9 节。

## 5. 内容指纹

判断"条目内容是否相同"用 `fingerprint = sha256(规范化 JSON)`。同步数据集里每个条目附带自己的 fingerprint、changedAt、deleted。

- 入站：`enable, listen, port, protocol, settings, streamSettings, sniffing, remark, total, expiryTime`；排除 `id / userId / up / down / clientStats`。
- 出站：`tag, protocol, sendThrough, settings, streamSettings, proxySettings, mux, targetStrategy`；排除 `id / up / down`；`sort` 单独参与顺序处理。
- 路由：`tag + canonicalRule(rawJson)`（复用现有函数，忽略 `ruleTag`）；`sort` 单独参与顺序处理。

注意现有 `inboundSyncSignature` 只覆盖热加载判断所需的字段（不含 remark/total/expiryTime），合并使用的指纹是更宽的集合，两者用途不同。

## 6. 同步协议

### 6.1 端点

| 端点 | 状态 | 说明 |
| --- | --- | --- |
| `GET /xui/node_sync/info` | 扩展 | 增加 `panelId`，保留 `app/version` |
| `GET /xui/node_sync/export` | 扩展 | 返回 `{app, version, panelId, sentAt, inbounds, outbounds, routingRules, state}` |
| `POST /xui/node_sync/receive-merge` | 新增 | 请求 `{senderId, dataset, state}`；校验 senderId 为已登记对端后，按同一合并逻辑处理再应用 |
| `receive-init` / `receive-apply` / `release` | 下线 | 保留路由返回 409 + 明确提示"已改为对等同步，请升级两端版本" |

### 6.2 对端关系

- **单向添加即可**：A 添加 B（需要 B 的地址和登录凭据），同步由 A 发起；每轮 A 先拉 B 的数据，再把合并结果推回 B，所以数据仍然是双向流动，B 不需要持有 A 的凭据。
- B 第一次收到来自 A 的 `receive-merge` 时，自动登记 A 为"被动对端"（无密码、不主动同步），在列表中可见、可删除。
- 删除被动对端后，若对方仍能登录本面板并推送，会重新出现；彻底断开需要同时删除对方的对端配置或修改本面板密码。这是有意的取舍：能登录管理面板的一方本就能直接改数据。
- 不支持自己添加自己（沿用现有 `isSelfPanelAddress` 检查），并额外校验 `panelId != 本面板`。

### 6.3 一轮同步（对每个对端，串行）

1. 登录对端，`GET /export`；校验 `app`、最低版本、`panelId == node.PeerId`。
2. 载入本端 `BaseData`（无则为空基线）。
3. `merge(base, local, remote) → M`。
4. `M` 与本端当前数据有差异才 `applyDataSet(M)`：DB 事务 → Xray API 热加载 → 失败标记待重启兜底。
5. `POST /receive-merge` 把 `M` 推回对端；对端用同样的合并逻辑（自己的 base/local 对 `M`）再合并一次并应用，吸收 export 之后的新改动。
6. 更新 `BaseData = M`、`LastSync = now`，记录本端同步摘要（每类新增/更新/删除数、hotReload、warnings）。
7. 任一步失败：该对端本轮终止，继续下一个对端；错误留在节点状态里展示。

并发控制：沿用全局 `syncOperationMu`（手动/自动互斥），一轮内对端串行；自动同步沿用现有 30s tick + 每端到期时间，只是方向从"推送"改为"拉取 + 推回合并结果"。

### 6.4 收敛性质

- 合并幂等：对合并结果再合并不产生变化。
- 两端各自更新基线，最坏情况多花一轮收敛（A 合并后，B 在下一轮拉到 A 的结果）。
- 多台链式：A 的改动经 B 传到 C 需要两个同步间隔；不保证一轮全网一致。
- export 与 receive-merge 之间的并发编辑由对端自己的那次合并吸收，不会丢。

## 7. 合并算法

每个条目在 B（base）/ L（local）/ R（remote）三份数据中各有 `{fingerprint, changedAt, deleted, payload}`。按 `kind + tag` 逐条：

1. `L.fp == R.fp` 且删除状态相同 → 取任一（已一致）。
2. L 与 B 完全相同（指纹、删除状态、时间都没变）→ 取 R。
3. R 与 B 完全相同 → 取 L。
4. 两边都改了 → 比较 `changedAt`，大者胜；相等时 `panelId` 字典序大者胜。
5. 结果为删除 → 本地删除该条目；否则写入 payload。
6. 基线中不存在的新条目：只有一边有 → 取那一边；两边都有且不同 → 按第 4 条。

### 7.1 顺序处理（出站、路由）

- 合并后按 `sort` 升序排列，`sort` 相同按 `tag` 字典序；
- 重排 `sort = 0..n-1` 写回；
- 新增规则保留自身的 `sort` 值参与排序，冲突时由上面的规则定序。

### 7.2 端口冲突（入站）

合并结果里出现不同 tag、同一端口：

- 保留 `changedAt` 更新的一条，另一条跳过；
- 记 warning，同步结果里展示"端口冲突已跳过 N 条"；
- 首次配对（并集合并）时最容易发生。

### 7.3 删除

- 删除写墓碑（`deleted=1` + 时间），与编辑对撞时同样按"后改的胜"；
- 删除后重建同一 tag：状态覆盖为 live + 新时间；
- 墓碑随数据集交换，对端据此删除本地条目。

## 8. 应用（写库 + 热加载）

- 复用现有 `applyDataSet`：整表替换入站/出站/路由；客户端流量按 email 保留；热加载按现有 signature 差异执行；失败 `SetToNeedRestart` 兜底。
- 入站归属本面板第一个用户（现有逻辑）。
- 合并结果与本地数据一致时跳过 apply：不写库、不碰 Xray。

## 9. 已知限制与取舍

1. 入站是合并最小单位：同一入站内两边各加不同客户端，后改的一条会整条覆盖另一条。v1 接受，后续可做客户端级合并（需要按协议解析 settings）。
2. 时间来自各面板本机时钟，需要两端都跑 NTP；在同一对两端编辑时间非常接近时可能判反，相等时用 panelId 兜底保证两端结果一致。
3. 后台自动续费/自动禁用也打时间戳：极端情况下会自动任务覆盖对端人工修改（仍然遵循"后写者胜"）。
4. 端口冲突直接跳过较早的一条，不自动改端口。
5. 流量计数不同步；删除客户端后其流量行随 apply 消失（与现状一致）。
6. 不保证"一轮内全网一致"，只保证持续同步下最终一致。
7. 降级不兼容：旧版本不认识 `receive-merge`；新版对旧版会明确报"请升级对端"。

## 10. 界面

节点同步页（`web/html/xui/node_sync.html`）改版：

- 列表列：顺序（上移/下移）、备注、地址、状态胶囊（面板地址错误 / 用户名密码错误 / 面板连接超时，保留）、最近同步时间、操作（立即同步 / 编辑 / 删除）。
- 顶部：添加对端、立即同步（全部）、自动同步间隔（5 / 10 / 30 / 60 分钟 / 24 小时 / 关闭）。
- 同步结果：每端新增 / 更新 / 删除条数、hotReload 状态、warnings（端口冲突、规则排序）。
- 被动对端（由对方发起同步）带标记，不能"立即同步"，可以删除。
- 移除：主副角色展示、初始化、下发同步、数据合并、覆盖主库按钮及对应文案。
- 翻译：`translate.zh_Hans.toml` / `translate.en_US.toml` 增删对应键。

## 11. 迁移与兼容

- 升级后 `sync_nodes` 原行直接作为对端，`Sort = id` 顺序；`PeerId` 在下一次检查/同步时从对端 `info` 补齐，未补齐前不允许同步。
- `BaseData` 为空 → 首轮为并集合并；原本主副两端数据相同则无变化。
- `sync_item_state` 迁移：现存入站/出站/路由全部 upsert `changed_at = 0`。
- `settings`：清空 `syncRole` / `syncMasterName`；`syncAutoInterval` 保留（原主面板的自动同步继续生效）。
- 旧端点保留路由返回 409，避免旧版对端静默覆盖数据。
- 最低对端版本校验：`info.version` 必须 ≥ 本次发布版本。

## 12. 测试计划

| 场景 | 预期 |
| --- | --- |
| 单端新增/修改/删除入站、出站、规则 | 对端下一轮一致 |
| 两边同时改同一个入站（不同内容） | 后改的胜；时间相同两端结果一致 |
| 一侧删除、一侧修改同一条 | 按时间取舍，两端一致 |
| 首次配对，两端数据不同 | 并集合并；端口冲突被跳过并提示 |
| 规则排序 | 合并后顺序稳定，两端一致 |
| 流量保留 | 同步后客户端 up/down 不丢、不清零 |
| 无变化同步 | 不写库、不重启 Xray |
| 有变化同步 | Xray API 热加载成功时进程 pid 不变 |
| 断网 / 改密码 / 超时 | 单端失败不影响其他对端，状态胶囊正确 |
| 三台链式 A-B-C | 两个间隔内 C 看到 A 的改动 |
| 26.14 主副配置升级 | 首轮同步无数据变化 |
| 旧版本对端 | 明确报错提示升级 |

## 13. 实施拆分

1. 基础：`sync_item_state` 表 + 迁移 + 各写入口打点 + 内容指纹。
2. 合并引擎：纯函数（base / local / remote → merged + 摘要）+ 单元测试。
3. 协议：export 扩展、receive-merge、info 增加 panelId、旧端点下线。
4. 同步引擎：对端串行同步、基线存储、手动/自动入口、被动对端登记。
5. 界面 + 翻译。
6. 迁移验证、双机/三机实测、README 更新、发版。

## 14. 待拍板

1. 旧端点处理：删除路由还是在保留的路由上返回 409（建议保留 409 提示）。
2. 同步 warning（端口冲突等）是否需要在节点列表常驻展示（建议放在同步结果弹窗 + 失败状态里）。
3. 是否接受"入站为最小合并单位"的 v1 限制（建议接受，客户端级合并后续再做）。