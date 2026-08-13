# 负载测试指南

## 前置要求

### 安装 k6

**macOS:**
```bash
brew install k6
```

**Linux:**
```bash
# Debian/Ubuntu
sudo gpg -k
sudo gpg --no-default-keyring --keyring /usr/share/keyrings/k6-archive-keyring.gpg --keyserver hkp://keyserver.ubuntu.com:80 --recv-keys C5AD17C747E3415A3642D57D77C6C491D6AC1D69
echo "deb [signed-by=/usr/share/keyrings/k6-archive-keyring.gpg] https://dl.k6.io/deb stable main" | sudo tee /etc/apt/sources.list.d/k6.list
sudo apt-get update
sudo apt-get install k6
```

## ⚠️ 安全第一！

**重要提示：** k6 **不会**根据服务器健康状况自动调整负载。即使服务器快要崩溃，它也会持续发送请求！

### 测试前准备

1. **在单独的终端监控服务器**：
   ```bash
   # 监控 CPU、内存、负载
   htop

   # 或者使用 Docker
   docker stats
   ```

2. **从安全测试开始**（推荐）：
   ```bash
   k6 run tests/load/k6-safe.js
   ```

   此测试具有**自动中止阈值**：
   - 错误率 > 10% 时中止
   - P95 延迟 > 5s 时中止

3. **准备好紧急停止**：
   ```bash
   # 按 Ctrl+C 立即停止 k6
   ```

### 警告信号（立即停止测试！）

| 指标 | 警告 | 危险 - 立即停止！ |
|------|------|------------------|
| **CPU** | > 80% | > 95% |
| **内存** | > 80% | > 90% |
| **负载平均值** | > CPU 核数 | > 2倍核数 |
| **错误率** | > 5% | > 10% |
| **响应时间** | > 2s | > 5s |

如果看到**危险**级别的值，**立即停止测试**（Ctrl+C）！

## 运行测试

### 1. 启动 API 服务器

```bash
# 构建并运行
make build
make run-server

# 或使用 Docker
docker run -d -p 127.0.0.1:1279:1279 -v poetry-data:/app/data \
  ghcr.io/ericismyeldestson/chinese-poetry-api:0.6.1
```

### 2. 运行负载测试

#### 🛡️ 安全渐进测试（从这里开始！）
```bash
k6 run tests/load/k6-safe.js
```

**特性：**
- 从 10 → 50 → 100 → 200 用户逐步增加
- 错误率 > 10% 或延迟 > 5s 时自动中止
- 适合首次测试

#### 基础负载测试
```bash
k6 run tests/load/k6-test.js
```

#### 🎯 最佳负载测试（推荐用于性能调优）
```bash
k6 run tests/load/k6-optimal.js
```

**特性：**
- 逐步测试 200 → 400 → 600 → 800 并发
- 找到系统的最佳性能点
- 适合性能调优和容量规划

**预期结果：**
- 400-600 并发：P95 < 1s，性能优秀
- 800 并发：P95 < 2s，可接受
- 1000+ 并发：延迟显著增加

#### ⚠️ 压力测试（谨慎使用！）
```bash
k6 run tests/load/k6-stress.js
```

**警告：** 此测试最高达到 2000 并发用户！
- 密切监控服务器
- 随时准备用 Ctrl+C 停止
- 可能导致临时服务降级

**预期结果：**
- 目标：1000-2000 并发用户
- 帮助识别最大容量
- 峰值时可能出现延迟增加和错误

#### ⚠️ 尖峰测试（极限负载！）
```bash
k6 run tests/load/k6-spike.js
```

**警告：** 突然 20 倍流量激增！
- 仅在压力测试成功后运行
- 服务器可能暂时无响应
- 准备好监控工具

**预期结果：**
- 模拟突然 20 倍流量增加
- 测试自动扩展和恢复能力

### 3. 自定义配置

#### 覆盖基础 URL：
```bash
k6 run -e BASE_URL=http://your-server:1279 tests/load/k6-test.js
```

#### 调整虚拟用户数：
```bash
# 使用更少用户的快速测试
k6 run --vus 10 --duration 30s tests/load/k6-test.js
```

#### 保存结果到文件：
```bash
k6 run --out json=results.json tests/load/k6-test.js
```

## 解读结果

### 关键指标

| 指标 | 优秀 | 可接受 | 较差 |
|------|------|--------|------|
| **http_req_duration (p95)** | < 200ms | < 500ms | > 1s |
| **http_req_duration (p99)** | < 500ms | < 1s | > 2s |
| **http_req_failed** | < 0.1% | < 1% | > 5% |
| **Requests/sec** | > 1000 | > 500 | < 100 |

### 示例输出

```
     ✓ list poems status 200
     ✓ random poem status 200
     ✓ search status 200

     checks.........................: 99.50% ✓ 29850    ✗ 150
     data_received..................: 245 MB 1.6 MB/s
     data_sent......................: 2.8 MB 18 kB/s
     http_req_blocked...............: avg=12.45µs  p(95)=25.3µs  p(99)=45.2µs
     http_req_duration..............: avg=125.34ms p(95)=285.4ms p(99)=456.7ms
     http_reqs......................: 30000  200/s
     iteration_duration.............: avg=5.12s    p(95)=5.45s   p(99)=5.89s
     iterations.....................: 6000   40/s
     vus............................: 100    min=0     max=200
```

## 优化建议

### 如果遇到高延迟：

1. **数据库已自动优化：**
   - SQLite PRAGMA 设置（WAL、缓存等）已自动应用
   - 连接池根据 CPU 核数自动调整
   - 无需手动配置 ✅

2. **调整连接池**（如有需要）：
   ```bash
   # docker-compose.yml
   environment:
     - DB_MAX_OPEN_CONNS=30
     - DB_MAX_IDLE_CONNS=15
   ```

3. **添加缓存层**（用于极限负载）：
   - Redis 用于查询结果
   - 内存缓存用于热数据

### 如果遇到高错误率：

1. 检查服务器日志中的错误
2. 监控系统资源（CPU、内存、磁盘 I/O）
3. 验证数据库连接池设置

## 持续监控

对于生产环境监控，考虑：
- **Grafana k6 Dashboard**：实时可视化
- **Prometheus**：指标收集
- **告警**：为性能降级设置告警
