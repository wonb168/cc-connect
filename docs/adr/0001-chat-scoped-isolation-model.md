---
status: accepted
---

# 以"群(Chat Space)"为顶层的隔离与会话记录模型

## 背景

原设计文档(《业务实体、用户、聊天空间隔离与会话记录方案》)提出四层层级
`业务实体 → 用户 → Chat Space → 会话/文件`,并配套 realpath 硬校验 + Agent
sandbox + bubblewrap 的硬隔离。经逐条评估现有 cc-connect 代码后,我们把该模型
**大幅收敛**,并作为本 fork 的落地基线。

## 决策

**顶层隔离单位是"群"(Chat Space, `Chat/<chatID>/`),不是"用户"。** 群是协作单位,
群内成员共享输入文件、输出文件、群记忆;只有[[对话历史]](Conversations)按用户
([[uid]]=union_id)细分。想避免上下文串味的场景应另开小群/私聊。

配套的具体取舍:

- **不建业务实体层**。业务实体 = 现有 `[[project]]` = [[work-dir]],不新增运行时
  entity 概念/目录。(前提:确认不存在"同一飞书 App 内多个互不信任客户"场景。)
- **不建用户顶层、无跨群个人记忆**。用户身份只在群内部用于区分 Conversations。
- **对内身份用 union_id(uid),对外交互用 open_id**。union_id 直接来自入站事件
  `sender.SenderId.UnionId`,零额外 API 成本;目录与 [[session-key]] 同源于 uid。
  切换会使历史 session 一次性失效(可接受)。飞书 API 交互(@人、名字解析、
  `allow_from`)保持 open_id。
- **Files 台账只记可观测的 input + delivered 两类**,放弃"Agent 生成但未发送的
  文件"(数据源不可得,不引入全盘扫描)。
- **Conversations 用 sessions.json(记真实 JSONL 绝对路径)+ turns.jsonl,不做软
  链接**(共享 work_dir / 跨机部署下软链接会断且无额外信息)。
- **只做软隔离(cwd + system prompt + memory loader),不做硬隔离**。

## 关键约束(决定上述取舍的现状事实)

1. 默认模式下**所有群共享同一个 work_dir、同一个 Claude Code 进程与 cwd**;Agent
   持有 Bash/Read 权限,realpath 校验拦不住 Agent 进程内部的 shell 越权——因此
   "硬隔离"在单 work_dir 模式下名不副实,真要硬隔离唯一可靠路径是每群一个独立
   work_dir(multi-workspace,binding key 改为 chatID)。
2. 输出文件由 Agent 自行选路径写盘,cc-connect **仅在** Agent 调 `cc-connect send`
   时才感知——故未发送的输出文件天然不可观测。
3. `userIDFromEvent` 的返回值同时喂给 SessionKey,故目录身份与 session 身份天然可
   同源,只要统一取值来源即可避免双身份线。

## 结果与后续

- **信任模型是本决策的地基**:仅本人及信任的同事使用,无不受信任外部用户。一旦将来
  接入不受信任用户,软隔离即失效,必须切换到"每群独立 work_dir + 独立 Agent 进程"
  的硬隔离路径。届时因顶层已是 `Chat/<chatID>/`,可直接将其提升为 work_dir 根,
  **目录结构无需重构**——这是选择"群为顶层"的一项隐含收益。
- 目录最终形状:
  ```
  <work_dir>/                    # = 业务实体 = project;CLAUDE.md 即 Agent 记忆
    Chat/<chatID>/
      Chat Memory.md             # 群级共享记忆
      Inputs/  Outputs/          # 群级共享
      Files/files.jsonl          # input + delivered
      User/<uid>/
        Conversations/
          sessions.json          # 真实 JSONL 绝对路径 + session 历史
          turns.jsonl            # 业务级每轮索引
  ```
