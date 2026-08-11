# 09 Research: 开源方案现状对比（含高 star 项目）

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/10
Status: resolved
Blocked by (open blockers): 0

Part of #1

## Question

Text2SQL / 语义层 / 数据 Agent 方向的开源方案现状对比——**不止 Wren AI / DB-GPT / Vanna，扩展到高 star 的相关项目**（候选：Chat2DB/AIE、MindsDB、LlamaIndex / LangChain 的 SQL agent 能力、Timescale pgai、其他 star > 3k 的相关项目）。

对比维度：定位与核心思想（Text2SQL RAG vs Semantic Layer vs Agent 框架）、是否可自托管、是否支持 MCP、权限/审计能力、对"多服务/多库/企业业务语义"的适配度、社区活跃度。

交付：5-8 个项目的对比表 + 结论（哪个最接近 "Enterprise Data Context Layer" 的方向，以及各自与自研的差距）。

下游票据：05（本体模型参考）、10（SQL 生成路线决策）。


## Answer

无现成开源项目等价于 Enterprise Data Context Layer：OpenMetadata（context + RBAC + 审计 + MCP，不做 SQL 执行）与 Wren AI（语义引擎 + 受管 Text2SQL + MCP，OSS 缺企业权限/审计）各占一半；Vanna 已归档、pgai 停维护、MindsDB 转向 → 自研薄 Go 工具层方向成立，语义语法借鉴 MDL/MetricFlow，guardrails 借鉴 Wren，不选型任何项目作执行引擎。

完整调研报告见 issue 评论与分支 research 下的文件。
