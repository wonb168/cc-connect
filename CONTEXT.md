# CONTEXT — cc-connect fork (wcz 定制需求)

本文件是术语表(glossary),只记录领域概念的规范定义,不含实现细节。

## 配置化总策略
五条需求的每个新能力都做成**独立配置开关**,但**默认全部开启(opt-out)**。理由:用户是唯一/主要使用者,追求开箱即用。保留独立开关的目的是——某条新逻辑出问题时可**单条快速关闭**以定位,而无需回退整个 fork。

⚠️ **已知张力**:默认全开意味着本 fork 相对上游原版**默认行为即不同**(footer/工具区/正文过滤均变化);rebase 上游后需留意行为差异来源。

⚠️ **需求 5(自动 handoff)默认开的破坏性风险**(用户已知情并选择默认开):到第 n 次 compact 会自动 `block` 压缩并强行 `/new` 切换会话;若交接文档生成不完整、路径约定失败或阈值不当,用户可能**在无预期下被切走会话、丢失上下文连续性**。且下述未验证假设成立与否直接决定该能力可用性——**落地时必须最先实测**:
> `PreCompact` 在"上下文已满"时触发;此刻 `block` 后再注入 `/handoff` 让当前会话跑一轮生成交接文档,会话是否还有足够上下文余量跑完而不立即再次触发 compact?

## 术语

### Handoff(交接)
将**当前会话**的压缩摘要 / 待办 / 关键上下文写成一份**交接文档**,随后开启一个**全新会话**(`/new`),并把交接文档作为新会话的第一条输入喂入。目的:用全新的、干净的上下文继续同一项工作,从而**避免 compact 反复累积**导致的上下文退化。

- 反例:handoff **不是**"把任务转交给另一个人或另一个 agent"。
- 交接文档由**当前会话自身**(复用 `handoff` skill 语义)生成,写到一个**由编排方指定的可预测绝对路径**(临时目录下),以便编排方读回并喂入新会话。
- 相关:[[compact]]、[[compact-threshold]]、[[compact-orchestrator]]

### Compact Orchestrator(compact 编排微服务)
一个独立于 cc-connect 源码的外部微服务,负责需求 5 的全部逻辑,零侵入 core。职责:
1. 作为 Claude Code CLI `PreCompact` hook 的后端:每次即将 compact 时被上报;
2. 按 `session_id` 维护 compact 计数,持有可配置阈值 n;
3. 前 n-1 次放行(hook 正常退出,compact 自然发生);第 n 次令 hook 返回 `{"decision":"block"}` 阻止本次压缩;
4. 拦截后经 cc-connect 的 `POST /send`(Unix socket)注入 [[handoff]] 指令(附可预测输出路径),待交接文档就绪后再注入 `/new` 并将文档内容作为新会话首条消息。

相关:[[handoff]]、[[compact-threshold]]

### Compact(上下文压缩)
Claude Code CLI 在上下文接近窗口上限时,对历史消息做的一次自动摘要压缩。cc-connect 侧以 `type:"result"` 且 `subtype:"compact"/"compaction"` 的事件识别它(见 `isCompactionResult`)。一次会话生命周期内可发生**多次** compact。

### Compact 阈值 / 第 n 次 compact(compact-threshold)
用户可配置的整数 n。当**同一会话**累计到"即将发生第 n 次 compact"时,系统不再让其自然 compact,而是触发一次 [[handoff]]。计数以单个会话(session_key)为单位。

### Reply Footer(回复状态行)
飞书等平台在每轮 Agent 回复**末尾**渲染的一到多段状态信息。当前已有字段:耗时(⏱)、model·effort·token 明细(out/in/cw/cr)·上下文占用百分比(ctx%)、工作目录。本次需求新增:**消耗金额([[cost]])**,并使显示形式**可配置**。

### Cost(消耗金额)
单轮回复的美元成本,口径**统一采用** Claude Code CLI result 事件里的 `total_cost_usd` 字段(官方账单口径,已含各类缓存折价)。**不**自行按 token 单价计算。此口径同时用于 [[reply-footer]] 展示与 [[usage-recorder]] 入库,二者必须一致。若走非官方计费的中转 provider 导致该字段为 0/缺失,则视为成本未知(不兜底计算)。

相关:[[reply-footer]]、[[usage-recorder]]、[[point]]

### Point(点)
[[reply-footer]] 中用于展示成本的**显示单位**。由 `total_cost_usd`(美金)乘以一个**可配置比例系数**换算得到(当前默认比例 = 1,即 1 点 = 1 美金)。

- `点`只是美金符号的一层**展示皮肤**,**仅作用于 footer 渲染**。
- [[usage-recorder]] 入库的永远是原始美金 `total_cost_usd`,**不受**比例系数影响;要点数在查询层再乘比例。
- footer 示例 `上下文x%-y点(消耗金额)-z秒` 中:`y点`=换算后点数,`(消耗金额)`=原始美金值,二者并列。

相关:[[cost]]、[[reply-footer]]

### Function-call 泄漏(function-call-leak)
不应展示给用户、却混入回复正文的 Claude 工具调用原生 XML 标记,形态为成对嵌套的 `<function_calls>…<invoke name="…">…<parameter …>…</parameter></invoke>…</function_calls>`。

- 过滤规则:**只删成对完整块**(开标签到闭标签间全部内容一并删除),不处理未闭合残片。
- 清洗时机:流式**中间帧尽力清 + 最终定稿帧保证干净**。中间帧可能短暂闪现半截标签(未闭合时无法配对删除),可接受;用户最终看到的完整回复必须干净。
- 落点:作为一条新正则加入 `core/markdown.go` 的清洗规则集,响应文本发送前应用。

### Tool Title(工具标题瞬态区)
飞书 rich card 在流式回复过程中展示的工具调用状态区,是一种**瞬态提示**,语义为"当前正在调用的工具"。

- **标题粒度**:纯工具名(`Bash`/`Read`/`Edit`),不含对象、不做中文动作映射。
- **合并计数**:当前批次内**连续相同**工具名合并为 `工具名 ×N`。
- **瞬态生命周期**:正文([[partial-text]])一开始输出即**收起**工具区;之后若再有工具调用,工具区**可再次出现**,再遇正文再收起(反复)。
- **计数作用域**:归属"当前这一批瞬态工具区";正文出现清除工具区时,连计数一并**归零**。`Bash→Bash→正文→Bash` 最后一个显示 `Bash ×1`,不做全轮累计。
- **定稿不留痕**:本轮回复结束(定稿)时工具区**完全消失**,卡片只剩最终正文 + [[reply-footer]],不保留工具摘要。
- 落点:`core/engine.go` 的 `EventToolUse` 分支(追加时做同名合并计数)+ 卡片渲染时依"是否已有正文 / 是否定稿"决定 `toolSteps` 是否传空;`ToolStep` 结构需增计数字段。

### Usage Recorder(用量入库模块)
[[compact-orchestrator]] 所在伴生微服务的第二个模块。接收 cc-connect core 每轮回复结束时的**出站 POST**({提问人身份, model, token 四元组, [[cost]]}),写入 PostgreSQL。是否启用、pg 连接、表结构均可配置。提问人身份来自 cc-connect(平台侧 sender),CLI hook 无法提供,故此模块的数据来源必须是 core 出站钩子而非 CLI hook。

## 隔离与会话记录模型

> 决策依据见 [ADR-0001](docs/adr/0001-chat-scoped-isolation-model.md)。以下为该模型引入的规范术语。

### 业务实体(Business Entity)
最大隔离边界,但**在本 fork 中不作为独立运行时概念存在**——它就等于现有的 `[[project]]`(一个 project = 一个 Engine = 一个固定 [[work-dir]])。不新建 `业务实体__entity_id/` 目录,不在 [[session-key]] 或 workspace binding 里增加 entity 维度。将来若真需要"一个 project 内多租户",再引入运行时实体识别,当前不预留。

- _Avoid_: 租户、tenant、entity_id(不要在代码里造这些运行时字段)

### Work Dir(工作目录 / 业务实体根)
一个 project 的 Agent 工作目录绝对路径(config 里 `[[project]]` 的 `work_dir`)。它**同时就是**该 [[业务实体]]的根,也是 Agent 进程的 cwd。work_dir 根下的 `CLAUDE.md` 就是"业务实体级 Agent 记忆",无需另建 `Agent Memory.md`。

### Chat Space(聊天空间 / 群)
消息发生的场所,以飞书 `chatID` 标识,是本模型的**顶层隔离与协作单位**。私聊、小群、大群都是 Chat Space。

- **群是协作单位,不隔离到人**:同一 Chat Space 内所有成员**共享**输入文件、输出文件、群记忆。想避免串味的场景应另开小群或私聊,而非在群内按人切分。
- 目录以 `Chat/<chatID>/` 命名。
- _Avoid_: 会话空间、房间、room

### UID(领域用户身份)
用户在本系统内的**规范身份**,取值为飞书 **union_id**(直接来自入站消息事件 `sender.SenderId.UnionId`,无需额外 API 查询)。用于一切**对内**的身份派生:[[session-key]] 的 user 段、`Chat/<chatID>/User/<uid>/` 目录。

- **对内用 uid,对外用 open_id**:凡"对飞书 API 说话"的动作(@人、resolveUserName、`allow_from` 授权)仍使用 open_id——那是与飞书交互的语言,不是领域身份。二者分工固定,不强行统一。
- 用户**不跨群**:无 `Users/<uid>/` 顶层目录,无跨群个人长期记忆。用户身份只在某个 Chat Space 内部用于区分[[对话历史]]。
- _Avoid_: userID、open_id(作领域身份时)、unionid(缩写统一为 uid)

### Session Key(会话键)
`core/session.go` 的 `ParseSessionKey` 定义的会话标识,默认形态 `platform:chatID:uid`。领域隔离身份(目录)与 session 身份**同源**——都由 uid 派生,不允许"目录一个 ID、session 另一个 ID"的双身份线。

- 切换到 union_id 会使**历史 session 失效一次**(旧 key 基于 open_id),fork/自用阶段可接受。

### Chat Memory(群记忆)
某个 Chat Space 的群级共享场景记忆,存于 `Chat/<chatID>/Chat Memory.md`。全群共享,取代原方案里按"用户+群"分别存的设计。memory loader **只**加载当前 Chat 的 Chat Memory + work_dir 级 CLAUDE.md,不注入其他 Chat 的记忆(软隔离手段之一)。

### Files Ledger(文件台账)
`Chat/<chatID>/Files/files.jsonl`,只记录 **cc-connect 实际可观测**的两类文件:
- `input`:用户上传文件落盘时(落盘路径从 `.cc-connect/attachments/` 迁到 `Chat/<chatID>/Inputs/`)。
- `delivered`:Agent 调 `cc-connect send --file` 发送时。

**不记录** Agent 生成但未发送的文件——该数据源在当前架构下不可得,不为它引入 work_dir 全盘扫描。

### Conversations Index(会话索引)
`Chat/<chatID>/User/<uid>/Conversations/`,cc-connect 自维护的业务级会话索引,与 Claude Code 自己的真实 JSONL **分离**:
- `sessions.json`:记录真实 JSONL 的**绝对路径**(session 创建时由 `AgentSessionID` + `findProjectDir` 固化写入)+ session 历史。**不做软链接**(共享 work_dir / 跨机部署下软链接会断且无额外信息)。
- `turns.jsonl`:业务级每轮索引(uid、chatID、user_text、关联 file_id、session_id、status、时间戳)。

### 软隔离(Soft Isolation)
本 fork 的**唯一**隔离手段(信任模型:仅本人及信任的同事使用,无不受信任外部用户)。由三件事构成:cwd 约定 + system prompt 软约束(默认只在当前群目录活动)+ memory loader 只加载当前 scope。

- **明确不做硬隔离**:realpath 硬校验、Agent sandbox、bubblewrap 全部不实现——它们防的是不受信任 Agent 的自主越权,该威胁在本信任模型下不存在。
- _Avoid_: 硬隔离、sandbox、bubblewrap(当前场景不适用)
