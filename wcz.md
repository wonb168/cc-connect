需求：

以下所有修改都要考虑后续github源头版本更新能跟着更新：

1. 飞书消息回复的末行显示可配置显示形式，如：上下文x%-y点（消耗金额）-z秒
2. 飞书消息回复过程中展示工具调用的标题，连续的相同标题次数合并，正文开始输出后清楚
3. 有时正文会混有<function call></function><function call></function>function call 等，不得显示
4. 消耗金额连同提问人，需保持到pg库，且支持配置
5. 多次compact后，能否在第n次compact前拦截，自动/handoff，再/new继续

以上哪些支持用单独的微服务解决，减少和cc-connect的源码耦合
