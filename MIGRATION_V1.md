# 从继承版 0.6.x 迁移到独立版 1.1.x

独立版第一次实际稳定数据与镜像发布为 **v1.1.0**，不延续上游 0.6.x 的版本号。原因是
数据身份、数据库 schema、查询边界和发布 namespace 都发生了破坏性变化。
`v1.0.0` 候选标签在创建发布物之前被可达漏洞门阻止并保留作为审计记录；它没有
对应的 GitHub Release、数据库资产或 GHCR 镜像。`v1.1.0` 已发布不可变数据
Release；镜像补丁 `v1.1.1` 修正发布自动标签对 `GPL-3.0-only` 的覆盖，并继续
使用、校验这份 `v1.1.0` 数据。

## 数据库必须重建或重新下载

- 0.6.x 的 `poetry.db` 使用 schema v1；独立版 v1 startup 只接受 schema v2。
- startup 不做静默原地升级。旧库会验证失败；没有有效 schema-v2 fallback 时，
  服务拒绝启动。
- 升级前先备份旧数据卷。正式发布后，可以让 startup 下载并校验 v1.1.0
  数据资产，或者从仓库固定的数据 submodule commit 完整运行 processor，再执行：

  ```bash
  sh scripts/verify-data-contract.sh \
    --database data/poetry.db \
    --source-report data/poetry.db.source-report.json
  ```

- 不要把 schema-v1 文件改名后放回数据卷；版本号、14 张必需表、SQLite
  完整性以及数据来源覆盖都必须通过验证。

## 数字 ID 不兼容

所有 poem 数字 ID 会在 v2 全量重建时重新分配。项目不提供 0.6.x ID 到
v1 稳定 ID 的映射，因为旧 ID 来自不稳定的加载/去重顺序，无法作为可靠的作品
身份。依赖方必须重新抓取 v1 数据，并用自身审核过的业务键重建外部关联；不得
假设相同数字仍指向同一首作品。

schema v2 在数据库内部新增跨简繁一致的 `canonical_id`，并为每个合并后的作品
保存一个或多个 source locator。最终身份前缀为 `cpa:poem:v2:sha256:`：标题、
正文、作者和朝代先统一到固定简体形态，分类和最终标题也在该形态上确定，再对带
字段/段落边界的编码取 SHA-256；SHA-512 fingerprint 用于碰撞复核。`zh-Hans`
成品直接取自 canonical，`zh-Hant` 成品只从同一 canonical 派生。v1 canonical
前缀不会被 v2 写入路径接受。

这些规则用于可重复构建、碰撞检查与来源审计，不会让旧的数字 ID 自动变得稳定。
新数字 ID 将 canonical SHA-256 的前 8 字节按大端整数读取，再保留其低 53 位
（零值映射为 1），从而适配 JavaScript 安全整数；在相同规范化作品内容下跨重建
保持稳定。如果标题、正文、作者或朝代被纠正，身份与数字 ID 会有意改变。
任何截断 ID 碰撞或同一 canonical 下的成品字段分歧都会使 release 构建失败，而
不会静默合并或任选一条来源。

独立版已随 `v1.1.0` 对外发布 schema v2 数据资产。开发期间曾生成的不含
canonical author 约束的 v2 预览库不属于兼容契约；migration、startup 和 release
verifier 都会明确拒绝它，必须改用已发布资产或用当前固定源与流水线重建。

## 体裁大类变化

诗词类型 ID 10–17 的 `category` 从误导性的“唐诗”改为朝代无关的“诗”。朝代仍由
作品的 `dynasty` 关系表达；调用方若用 `category=唐诗` 判断唐代作品，应迁移为
`category=诗` 与 `dynasty=唐` 的组合条件。

## 查询兼容性变化

- `GET /api/v1/poems/random?char=…` 已移除，返回 `410 Gone`。这避免为单字
  子串随机查询执行不可控的整库扫描。
- REST 与 GraphQL 的 `all`、`title`、`content` 搜索词至少需要 3 个 Unicode
  字符（按 runes 计数）；更短的查询返回客户端错误。`author` 查询不适用这条
  FTS 短查询限制。
- 调用方应把 `400` 与 `410` 作为明确的契约结果处理，而不是无条件重试。

## 镜像与数据 namespace

- 容器镜像迁移到 `ghcr.io/ericismyeldestson/chinese-poetry-api:1.1.1`。
- 数据资产迁移到本仓库 `v1.1.0` release；startup 默认不会下载上游 release。
- `v1.1.1` 是只含镜像修正的 patch tag，仍绑定 `v1.1.0` 数据 release；不会复制
  或重建一套同内容的 patch 数据资产。
- 镜像 tag、数据 release、schema 和 release manifest 必须成套核对。不要使用
  可变 `latest` 作为部署依据。

升级时建议先在新数据卷上完成 schema/来源验证和 API 契约测试，再切换流量。
旧数据卷只用于可回滚备份，不能与 v1 容器混用。
