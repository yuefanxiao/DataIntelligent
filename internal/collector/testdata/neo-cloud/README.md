# 采集器 golden test 语料：neo-cloud 真实迁移文件 + GORM 模型文件
#
# 本目录是 ~/cloud/neo-cloud（内部业务仓库）的真实语料拷贝，作为
# 采集器 golden test 集（ADR-0007「真实迁移语料即测试集」）：
# 同输入同输出（确定性），覆盖 DDL 形态：
#
#   bss-wallet      schema 前缀 + CHECK 枚举 + 外键引用 + 序列默认值 +
#                   ALTER ADD COLUMN/ADD CONSTRAINT + 增量迁移
#   bss-subscription schema 前缀 + 多文件增量迁移 + GORM 关联（slice 字段）
#   iam             public schema + 9 个迁移文件 + 大量 ALTER（含
#                   ALTER COLUMN TYPE）+ DROP TABLE + DO 块动态 DDL +
#                   模型子目录（internal/data/invite/）
#
# 更新方式：从 neo-cloud 对应路径重新拷贝（保持相对布局一致）；
# 变更后同步更新 ../golden/ 下对应期望文件。
