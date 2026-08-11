# 企业级 Data Intelligence Layer 方案讨论总结

## 一、背景

当前团队希望建设一个面向生产环境的数据查询能力，让非研发角色（例如产品经理、运营、业务人员）能够安全、自助地查询和分析生产数据。

目前已有的方式：

```
产品需求
   |
   v
开发人员理解业务
   |
   v
Coding Agent 辅助查询
   |
   v
查看代码 / 服务关系 / 数据库结构
   |
   v
编写 SQL
   |
   v
执行查询
```

这种方式依赖开发人员，并且每次 Agent 都需要重新理解整个业务上下文。

因此希望探索：

* 是否可以安全开放生产数据查询能力？
* 是否可以让 Agent 提前理解企业业务和数据关系？
* 是否存在成熟的 Text2SQL / Data Agent 方案？
* 是否应该构建 Agent，还是构建底层数据能力 Tool？

---

# 二、当前核心痛点

## 痛点 1：生产数据库访问受限

目前：

* 产品经理没有数据库访问权限
* 查询需求必须依赖开发
* 开发需要人工执行 SQL 或借助 Coding Agent

问题：

* 效率低
* 开发成为数据查询瓶颈
* 产品无法自主探索数据

---

## 痛点 2：Agent 每次都是新的上下文

当前 Coding Agent 查询流程：

```
用户问题：

"昨天支付失败率为什么上涨？"


Agent:

1. 找服务
2. 找数据库
3. 找 Pod
4. 查看代码
5. 查看 ORM
6. 查看 migration
7. 推断表结构
8. 编写 SQL
```

大量时间消耗在：

* 业务理解
* 服务拓扑发现
* 数据模型发现

而不是 SQL 本身。

---

## 痛点 3：数据库 Schema 缺少业务语义

数据库：

```
payment_order

status
amount
user_id
created_at
```

但是业务知道：

```
status=1 代表支付成功
status=2 代表支付失败

amount 是订单金额

failure_rate 是失败订单 / 总订单
```

数据库存储结构 ≠ 企业业务知识。

---

# 三、讨论方向 1：如何安全暴露数据库能力？

## 初步方案：直接给 Agent 数据库账号

不推荐。

例如：

```
Agent
 |
 postgres://username/password
 |
 Production DB
```

风险：

* SQL 注入
* DELETE / UPDATE 风险
* 数据泄露
* 查询影响生产

---

## 推荐方案：Data Gateway / MCP Tool

架构：

```
Agent

 |
 MCP

 |
Data Gateway

 |
-----------------
SQL Parser
Permission
Audit
Result Mask
Query Limit
-----------------

 |
Readonly Database User

 |
Database Replica
```

核心思想：

不要暴露数据库连接。

暴露经过控制的能力。

---

## Tool 不应该只有 execute_sql

低级接口：

```
execute_sql(sql)
```

能力有限。

更合理：

### 业务查询能力

```
search_business_concept()

```

例如：

输入：

```
支付失败
```

返回：

```
概念:
Payment Failure

所属服务:
payment-service

数据来源:
payment_order

相关指标:
failure_rate
```

---

### 指标定义能力

```
get_metric_definition()
```

例如：

```
GMV
```

返回：

```
GMV =
已支付订单金额
排除退款
```

---

### 查询规划能力

```
create_query_plan()
```

返回：

```
需要:

PaymentOrder

时间:

昨天

指标:

failure_rate
```

---

### 最终执行

```
execute_query()
```

---

# 四、讨论方向 2：如何避免 Agent 重复理解业务？

核心结论：

不要让 Agent 每次探索代码。

应该建立：

## Enterprise Data Context Layer

即：

企业数据语义层。

结构：

```
Service

 |
Database

 |
Table

 |
Column

 |
Metric

 |
Business Concept
```

形成企业数据知识。

---

例如：

```
payment-service

拥有:

payment_db

包含:

payment_order

定义:

支付订单

指标:

支付成功率

事件:

PaymentSuccessEvent
```

---

这实际上与以下技术方向一致：

* Semantic Layer
* Ontology（本体）
* Knowledge Graph
* GraphRAG
* UModel

---

# 五、Text2SQL 开源方案调研

## 方案 1：Vanna AI

定位：

传统 Text2SQL RAG。

流程：

```
用户问题

↓

Schema Retrieval

↓

LLM

↓

SQL

```

优点：

* 简单
* 快速落地

缺点：

* 主要解决 SQL 生成
* 不解决复杂业务理解

适合：

单数据库、小规模数据。

---

## 方案 2：DB-GPT

定位：

AI Database Application。

包含：

* Text2SQL
* Agent
* RAG
* 数据分析能力

优点：

比较完整。

缺点：

对于：

* 多服务
* 多数据库
* 企业业务关系

仍需要大量改造。

---

## 方案 3：Wren AI

重点研究。

核心不是 Text2SQL。

核心：

## Semantic Layer

即：

数据库之上的业务模型。

---

# 六、Wren AI 核心思想

数据库：

```
payment_order

status
amount
```

通过 MDL：

转换为：

```
PaymentOrder

业务:
支付订单

字段:

payment_status

状态:

SUCCESS
FAILED


指标:

payment_success_rate

=
success / total
```

---

MDL 提供：

## 1. Entity

告诉 Agent：

这个对象是什么。

例如：

```
payment_order

↓

支付订单
```

---

## 2. Relationship

告诉 Agent：

实体如何关联。

例如：

```
PaymentOrder

belongs_to

User
```

---

## 3. Metric

定义企业指标。

例如：

```
支付成功率

=
成功支付订单数 /
总支付订单数
```

---

Wren 的执行方式：

不是：

```
LLM直接猜SQL
```

而是：

```
用户问题

↓

语义解析

↓

匹配 Semantic Model

↓

展开 Metric

↓

生成 SQL

↓

执行
```

类似：

```
高级语言

↓

Compiler

↓

机器码
```

---

# 七、是否应该直接使用 Claude Code / Codex 调工具？

讨论结果：

可以，但不是最终方案。

原因：

## Tool 只有能力，没有知识

例如：

```
execute_sql()
```

只能告诉 Agent：

"你可以查询"

但是不知道：

* 什么是支付
* 什么是订单
* 什么是收入
* 指标怎么算

---

## 没有 Semantic Layer 会导致：

### 1. 上下文爆炸

需要加载：

* 所有数据库
* 所有表
* 所有代码
* 所有服务关系

### 2. Agent 结果不一致

Claude：

```
订单金额 = amount
```

Codex：

```
订单金额 = paid_amount
```

无法保证统一。

Semantic Layer 提供：

企业唯一解释。

---

# 八、是否应该做 Agent？

讨论出现关键转变：

## 不应该优先做 Data Agent

更应该做：

# Enterprise Data Intelligence Tool

原因：

Agent 是消费者。

数据能力才是资产。

---

目标架构：

```
                 Claude Code
                 Codex
                 ChatGPT
                 DeerFlow
                 LangGraph


                      |

                     MCP


                      |

        Enterprise Data Intelligence Layer


        --------------------------------

        Semantic Layer

        Knowledge Graph

        Metadata RAG

        SQL Planner

        Permission Engine

        Audit System


        --------------------------------


                      |

                 Production Data

```

---

# 九、为什么推荐 LangGraph，而不是 ReAct / DeepAgent / DeerFlow？

## ReAct Agent

优势：

* 简单
* 会调用工具

问题：

每次重新探索。

适合：

未知问题探索。

不适合：

企业固定知识。

---

## DeepAgent

优势：

* 多 Agent
* 长任务
* 文件操作

适合：

代码生成、复杂研究。

问题：

解决的是 Agent 能力，不是企业知识问题。

---

## DeerFlow

优势：

Deep Research。

适合：

互联网资料搜索。

问题：

数据库场景需要：

* 权限
* 审计
* 确定性

不是研究问题。

---

## LangGraph

更适合作为上层编排。

原因：

Data Query 是流程型任务。

例如：

```
用户问题

↓

意图识别

↓

业务知识检索

↓

Semantic Resolution

↓

SQL生成

↓

SQL审核

↓

执行

↓

解释
```

这是 Graph Workflow。

不是完全自由 Agent。

---

# 十、最终推荐架构

推荐：

```
                 Any Agent


                    |

                   MCP


                    |

      Enterprise Data Context Layer


 ------------------------------------------------

 1. Semantic Layer
    (Wren AI 思想)


 2. Knowledge Graph
    (UModel / Ontology 思想)


 3. Metadata RAG


 4. SQL Compiler


 5. Permission Engine


 6. Audit System


 ------------------------------------------------


                    |

              Production Data

```

---

# 十一、最终产品定位

不要定位：

```
Text2SQL Agent
```

因为太低层。

不要定位：

```
Chat Database
```

因为只解决查询。

更合理：

```
Enterprise Data Context Platform

或者：

Enterprise Data Intelligence Layer
```

核心价值：

> 让任何 Agent 都可以安全、准确地理解企业数据。

---

# 十二、下一步建议

MVP 不建议直接做完整 Agent。

建议：

## Phase 1

建设 Data MCP Gateway

能力：

* schema 查询
* readonly SQL
* audit
* permission

---

## Phase 2

建设 Metadata Knowledge

自动采集：

* DB schema
* ORM
* migration
* protobuf/API
* 服务关系

生成：

```
企业数据知识图谱
```

---

## Phase 3

加入 Semantic Layer

类似：

Wren MDL。

维护：

* 指标
* 业务实体
* 数据关系

---

## Phase 4

开放给 Agent

支持：

* Claude Code
* Codex
* ChatGPT
* LangGraph
* DeerFlow

最终形成：

一个企业级 Data Context Provider。
