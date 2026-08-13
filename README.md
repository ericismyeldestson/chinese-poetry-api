<div align="center">

<img src="docs/icon.png" alt="chinese-poetry" height="100px">

<h2>中国古诗词 API 服务</h2>

[![CI](https://github.com/ericismyeldestson/chinese-poetry-api/actions/workflows/test.yml/badge.svg)](https://github.com/ericismyeldestson/chinese-poetry-api/actions/workflows/test.yml)
[![Container](https://img.shields.io/badge/container-GHCR-blue?logo=github)](https://github.com/ericismyeldestson/chinese-poetry-api/pkgs/container/chinese-poetry-api)
[![pre-commit](https://img.shields.io/badge/pre--commit-enabled-brightgreen?logo=pre-commit)](https://github.com/pre-commit/pre-commit)
[![License](https://img.shields.io/github/license/ericismyeldestson/chinese-poetry-api)](LICENSE)

由 `ericismyeldestson` 独立维护的 Go 中国古诗词 API，支持 REST、GraphQL 和简繁文本变体。
本项目保留上游 Git/GPL 历史，但拥有独立的构建、数据和发布治理；详见
[INDEPENDENCE.md](INDEPENDENCE.md)、[UPSTREAM_HISTORY.md](UPSTREAM_HISTORY.md) 和 [NOTICE](NOTICE)。

> 数据质量声明：当前语料是互联网汇编数据，不是权威校勘本。朝代、作者、异文和简繁转换必须通过本项目的数据质量门禁；用于研究、出版或训练标签前仍需复核。

</div>

## 当前能力

- Go API 服务与 SQLite FTS5 搜索，提供 REST 和 GraphQL 接口
- 同一 schema 中保存 `zh-Hans` 与 `zh-Hant` 两套文本变体
- schema v2 为诗词建立跨简繁的稳定 canonical identity，并保存逐条来源 locator
- 固定上游数据 commit；release manifest 记录数据源、生成器与实际行数
- 请求参数、分页、GraphQL body/complexity、服务端 timeout 与限流边界
- 非 root、只读根文件系统、最小 capability 的容器运行配置

## 快速开始

### 使用 Docker（推荐）

独立版的第一个稳定候选是 **v1.0.0**。当前仓库尚未发布对应的 GHCR
镜像和数据库 release，所以下面的命令只适用于 v1.0.0 正式发布之后；
不能把本地镜像构建成功当作远端 release 已存在。

```bash
docker run -d --read-only --cap-drop ALL \
  --security-opt no-new-privileges \
  -p 127.0.0.1:1279:1279 \
  -v poetry-data:/app/data \
  ghcr.io/ericismyeldestson/chinese-poetry-api:1.0.0
```

完整配置参见 [docker-compose.yml](docker-compose.yml)。Compose 同时保留了
`build: .`，便于在 release 前验证本地镜像；容器首次启动仍必须取得一个通过
schema v2、checksum 和 SQLite 完整性验证的数据资产。容器以 UID/GID 10001
运行；推荐使用命名卷，若改用宿主机 bind mount，目录必须对该 UID/GID 可写。

### 使用 Makefile

```bash
make help          # 查看所有可用命令
make build         # 构建项目
make process-data  # 处理数据
make run-server    # 启动服务
```

从固定数据源完整重建后，可以先验证再启动：

```bash
sh scripts/verify-data-contract.sh \
  --database data/poetry.db \
  --source-report data/poetry.db.source-report.json
make run-server
```

### 克隆仓库

本项目使用 Git Submodules 管理诗词数据，推荐使用以下命令快速克隆：

```bash
# 完整克隆（包含 submodules）
git clone --recurse-submodules https://github.com/ericismyeldestson/chinese-poetry-api.git
```

如果已经克隆了仓库，可以单独更新 submodules：

```bash
git submodule update --init --depth 1
```

## API 使用

### 多语言支持

所有接口支持 `lang` 参数切换简繁体：

|  参数值   |       说明       |
| :-------: | :--------------: |
| `zh-Hans` | 简体中文（默认） |
| `zh-Hant` |     繁体中文     |

### REST API

```bash
# 健康检查
curl "http://localhost:1279/api/v1/health"

# 统计信息
curl "http://localhost:1279/api/v1/stats"

# 简体中文（默认）
curl "http://localhost:1279/api/v1/poems"

# 繁体中文
curl "http://localhost:1279/api/v1/poems?lang=zh-Hant"

# 诗词列表（带过滤，与 /poems/random 使用同一组参数）
curl "http://localhost:1279/api/v1/poems?dynasty=唐"
curl "http://localhost:1279/api/v1/poems?author=李白&page=2&page_size=50"
curl "http://localhost:1279/api/v1/poems?dynasty_id=1&type_id=11&type_id=12" # 多个 type_id 为「或」关系

# 参数校验：未知或非法的查询参数返回 400，而不是被忽略
curl "http://localhost:1279/api/v1/poems?dynastyId=1"   # 400，未知参数
curl "http://localhost:1279/api/v1/poems?page_size=500" # 400，超出上限 100

# 搜索诗词
curl "http://localhost:1279/api/v1/poems/search?q=静夜思"

# 随机诗词
curl "http://localhost:1279/api/v1/poems/random"

# 随机诗词（带过滤）
curl "http://localhost:1279/api/v1/poems/random?author=李白"
curl "http://localhost:1279/api/v1/poems/random?type=五言绝句"
curl "http://localhost:1279/api/v1/poems/random?author=李白&type=五言绝句"
curl "http://localhost:1279/api/v1/poems/random?author=李白&type=五言绝句&dynasty=唐"
curl "http://localhost:1279/api/v1/poems/random?author=李白&dynasty=唐&type=五言绝句&type=七言绝句&type=五言律诗"

# 作者列表
curl "http://localhost:1279/api/v1/authors?page=1&page_size=20"

# 作者详情
curl "http://localhost:1279/api/v1/authors/<author_id>"

# 朝代列表
curl "http://localhost:1279/api/v1/dynasties"

# 朝代详情
curl "http://localhost:1279/api/v1/dynasties/1"

# 诗词体裁列表
curl "http://localhost:1279/api/v1/types"

# 诗词体裁详情
curl "http://localhost:1279/api/v1/types/10"
```

### GraphQL API

端点：`http://localhost:1279/graphql`

```graphql
# 繁体中文查询
query {
  poems(lang: ZH_HANT, pageSize: 10) {
    edges {
      node {
        title
        content
        author {
          name
        }
      }
    }
    totalCount
  }
}

# 搜索诗词
query {
  searchPoems(query: "静夜思", searchType: TITLE) {
    edges {
      node {
        title
        author {
          name
        }
      }
    }
  }
}

# 统计信息
query {
  statistics {
    totalPoems
    totalAuthors
    poemsByDynasty {
      dynasty {
        name
      }
      count
    }
  }
}
```

## 搜索功能

|   类型    |       说明       |             示例             |
| :-------: | :--------------: | :--------------------------: |
|   `all`   | 全文搜索（默认） |         `?q=明月光`          |
|  `title`  |     标题搜索     |    `?q=静夜思&type=title`    |
| `content` |     内容搜索     | `?q=床前明月光&type=content` |
| `author`  |     作者搜索     |    `?q=李白&type=author`     |

`all`、`title` 和 `content` 搜索至少需要 3 个 Unicode 字符（runes）；过短
查询返回 `400`，以避免对整库执行昂贵的短子串扫描。独立版 v1 已移除旧的
`?char=` 单字随机检索兼容入口，该参数返回 `410 Gone`。迁移说明见
[MIGRATION_V1.md](MIGRATION_V1.md)。

## 数据集

初始语料基于固定版本的 [chinese-poetry](https://github.com/chinese-poetry/chinese-poetry) 数据集。
每个正式数据 release 必须附带 `data-source-manifest.json`，其中的简繁诗词/作者
行数、逻辑摘要和原始数据库 SHA-256 与 release 数据库逐项核对；
`poetry.db.source-report.json` 则逐文件记录加载/排除决策，并汇总接受/拒绝的源
记录。固定源基线为 389,341 条候选记录：接受 388,857 条、拒绝 484 条；最终
产品为简繁各 372,239 首、13,814 位 canonical 作者，每种语言保留 388,857 条
来源 witness。冻结源码已经从零完整构建两次，两份 2,652,225,536-byte SQLite
数据库及来源报告均逐字节一致，并分别通过完整契约、SQLite quick check 和外键
检查；远程 `v1.0.0` Release 仍未发布。canonical v2 先把身份字段转换为固定简体
形态，再做分类、标题选择和作品合并。有意更换源 revision、转换器或身份算法时，
必须审核并更新 contract，并再次完成双重建哈希门禁。运行时统计以 API `/stats`
为准。

## 已知数据边界

- 新版 poem 数字 ID 由 `cpa:poem:v2:sha256:` canonical identity 确定。标题、
  作者、朝代和正文先统一到固定简体形态，分类与标题选择也只在该形态上进行；
  `zh-Hans` 成品直接取自 canonical，`zh-Hant` 成品由同一 canonical 派生。因此
  同一规范化内容可跨重建、简繁源写法、简繁产品和源文件顺序保持稳定；内容、
  作者或朝代被纠正时，ID 会随作品身份改变。
- 作者工程身份现以“规范化姓名 + 朝代”复合键区分，作品朝代与作者朝代的
  不一致会使数据构建失败。仅按姓名查询而跨多个朝代时，REST 返回 `409`，
  客户端必须补 `dynasty`/`dynasty_id` 或直接用 `author_id`。这仍不是历史人物学上的
  终局身份模型：别名、同朝代同名异人与存疑归属仍需来源校勘。固定语料全量构建
  已验证简繁各 13,814 位 canonical 作者，并由契约强制每首作品的作者朝代与作品
  朝代一致；这仍只证明当前工程规则的一致性，不是历史人物归属的文献学定论。
- canonical 合并和简繁转换是工程身份规则，不是版本学上的定本判定。被合并的每个
  源记录仍以独立 locator 保留为 provenance witness；同一 canonical 若产生不同
  成品字段，构建会拒绝写入而不是任选一条。该数据库适合展示、娱乐检索和原型；
  不应在没有来源复核时作为学术引用、出版底本、训练金标或商业再分发的唯一依据。

## 致谢

- 数据来源：[chinese-poetry](https://github.com/chinese-poetry/chinese-poetry)
- 简繁转换：[hanconv/go v0.3.0](https://github.com/fhluo/hanconv/tree/go/v0.3.0/go)，
  内嵌词典来自 [OpenCC](https://github.com/BYVoid/OpenCC)

许可证、数据来源和再分发通知见 [LICENSE](LICENSE) 与 [NOTICE](NOTICE)。
