# 方案D: 内置Web监控面板 🌐

## 🎯 概述

为不想使用 Grafana 的用户提供的轻量级方案：在 mosdns-x 服务内嵌一个简单的 Web 监控面板，直接在浏览器查看所有监控指标。

---

## ⚡ 特点

- ✅ **无需额外基础设施** - 不需要 Prometheus 或 Grafana
- ✅ **开箱即用** - 访问 `http://localhost:8080/health-ui` 即可
- ✅ **实时刷新** - 每5秒自动更新数据
- ✅ **响应式设计** - 支持桌面和移动设备
- ✅ **轻量级** - 单个 HTML 页面，不依赖外部库
- ✅ **彩色状态** - 绿色=健康，黄色=警告，红色=严重

---

## 📊 效果预览

```
┌─────────────────────────────────────────────────────────────┐
│  mosdns-x 监控面板                           最后更新: 刚刚  │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  整体健康评分: 95/100 ✅                                      │
│  ████████████████████░░                                      │
│                                                               │
│  ┌─────────────────────┐  ┌─────────────────────┐          │
│  │ 会话清理失败率       │  │ 数据库连接使用率     │          │
│  │ 0.0% ✅             │  │ 65% ✅              │          │
│  │ 健康: < 1%          │  │ 健康: < 80%         │          │
│  └─────────────────────┘  └─────────────────────┘          │
│                                                               │
│  ┌─────────────────────┐  ┌─────────────────────┐          │
│  │ 限速器内存条目数     │  │ 凭证计数不匹配       │          │
│  │ 1,234 ✅            │  │ 0 ✅                │          │
│  │ 健康: < 2048        │  │ 健康: 0             │          │
│  └─────────────────────┘  └─────────────────────┘          │
│                                                               │
│  详细信息:                                                    │
│  • 活动会话: 15                                              │
│  • 数据库连接: 10 / 32                                       │
│  • 启动时间: 2h 34m                                          │
│  • 服务状态: 运行中                                          │
└─────────────────────────────────────────────────────────────┘
```

---

## 🛠️ 实施步骤

### 步骤1: 复用方案B的健康检查代码（如果还没实施）

如果你还没有实施方案B，先按 [MONITORING_QUICKSTART.md](MONITORING_QUICKSTART.md) 创建：
- `internal/control/health.go`
- `internal/control/mysql_health.go`

如果已经实施，直接跳到步骤2。

---

### 步骤2: 添加 Web UI 处理器

创建 `internal/controlapi/health_ui.go`：

```go
package controlapi

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"time"
)

//go:embed templates/health.html
var healthUIFS embed.FS

// HealthUIResponse 是 JSON API 响应
type HealthUIResponse struct {
	Timestamp         time.Time `json:"timestamp"`
	OverallScore      int       `json:"overall_score"`
	SessionCleanup    Metric    `json:"session_cleanup"`
	DBConnections     Metric    `json:"db_connections"`
	RateLimiterMemory Metric    `json:"rate_limiter_memory"`
	CredentialCount   Metric    `json:"credential_count"`
	Details           Details   `json:"details"`
}

type Metric struct {
	Value      float64 `json:"value"`
	Threshold  float64 `json:"threshold"`
	Status     string  `json:"status"` // "healthy", "warning", "critical"
	Unit       string  `json:"unit"`
	Label      string  `json:"label"`
}

type Details struct {
	ActiveSessions   int       `json:"active_sessions"`
	DBConnectionsInUse int     `json:"db_connections_in_use"`
	DBConnectionsOpen  int     `json:"db_connections_open"`
	Uptime           string    `json:"uptime"`
	ServiceStatus    string    `json:"service_status"`
}

// ServeHealthUI 返回 HTML 监控页面
func (h *Handler) ServeHealthUI(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(healthUIFS, "templates/health.html")
	if err != nil {
		http.Error(w, "Failed to load template", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ServeHealthData 返回 JSON 监控数据
func (h *Handler) ServeHealthData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	
	// 获取健康数据（假设你已经实施了方案B的健康检查）
	health, err := h.opts.Control.GetHealthStatus(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	// 构建响应
	resp := HealthUIResponse{
		Timestamp: time.Now(),
		SessionCleanup: Metric{
			Value:     health.SessionCleanupErrors,
			Threshold: 1.0,
			Status:    getStatus(health.SessionCleanupErrors, 1.0, 5.0, false),
			Unit:      "%",
			Label:     "会话清理失败率",
		},
		DBConnections: Metric{
			Value:     float64(health.DBConnectionsInUse) / float64(health.DBConnectionsOpen) * 100,
			Threshold: 80.0,
			Status:    getStatus(float64(health.DBConnectionsInUse)/float64(health.DBConnectionsOpen)*100, 80.0, 90.0, false),
			Unit:      "%",
			Label:     "数据库连接使用率",
		},
		RateLimiterMemory: Metric{
			Value:     float64(health.RateLimiterEntries),
			Threshold: 2048,
			Status:    getStatus(float64(health.RateLimiterEntries), 2048, 3500, false),
			Unit:      "",
			Label:     "限速器内存条目数",
		},
		CredentialCount: Metric{
			Value:     float64(health.CredentialCountMismatches),
			Threshold: 0,
			Status:    getStatus(float64(health.CredentialCountMismatches), 0, 1, true),
			Unit:      "",
			Label:     "凭证计数不匹配",
		},
		Details: Details{
			ActiveSessions:     health.ActiveSessions,
			DBConnectionsInUse: health.DBConnectionsInUse,
			DBConnectionsOpen:  health.DBConnectionsOpen,
			Uptime:            health.Uptime.String(),
			ServiceStatus:     "运行中",
		},
	}
	
	// 计算整体健康评分
	resp.OverallScore = calculateOverallScore(resp)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// getStatus 根据值和阈值返回状态
// higher_is_worse: true 表示值越高越差（如错误率），false 表示值越高越差但不是错误
func getStatus(value, warningThreshold, criticalThreshold float64, exact bool) string {
	if exact {
		// 对于"必须为0"的指标（如凭证不匹配）
		if value == 0 {
			return "healthy"
		}
		return "critical"
	}
	
	if value < warningThreshold {
		return "healthy"
	} else if value < criticalThreshold {
		return "warning"
	}
	return "critical"
}

// calculateOverallScore 计算整体健康评分（0-100）
func calculateOverallScore(resp HealthUIResponse) int {
	score := 100
	
	// 每个 critical 状态 -25 分
	// 每个 warning 状态 -10 分
	metrics := []Metric{
		resp.SessionCleanup,
		resp.DBConnections,
		resp.RateLimiterMemory,
		resp.CredentialCount,
	}
	
	for _, m := range metrics {
		switch m.Status {
		case "critical":
			score -= 25
		case "warning":
			score -= 10
		}
	}
	
	if score < 0 {
		score = 0
	}
	
	return score
}
```

---

### 步骤3: 创建 HTML 模板

创建 `internal/controlapi/templates/health.html`：

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>mosdns-x 监控面板</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-box: border-box;
        }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            background: #0f1117;
            color: #e4e4e7;
            padding: 20px;
            line-height: 1.6;
        }
        
        .container {
            max-width: 1200px;
            margin: 0 auto;
        }
        
        header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 30px;
            padding-bottom: 20px;
            border-bottom: 2px solid #1e222e;
        }
        
        h1 {
            font-size: 28px;
            font-weight: 600;
        }
        
        .last-update {
            color: #a1a1aa;
            font-size: 14px;
        }
        
        .overall-score {
            background: #171a23;
            padding: 30px;
            border-radius: 8px;
            margin-bottom: 30px;
            border: 1px solid #1e222e;
        }
        
        .overall-score h2 {
            font-size: 20px;
            margin-bottom: 15px;
            font-weight: 500;
        }
        
        .score-value {
            font-size: 48px;
            font-weight: 700;
            margin-bottom: 10px;
        }
        
        .score-bar {
            width: 100%;
            height: 12px;
            background: #1e222e;
            border-radius: 6px;
            overflow: hidden;
        }
        
        .score-fill {
            height: 100%;
            transition: width 0.5s ease, background-color 0.5s ease;
            border-radius: 6px;
        }
        
        .metrics-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
            gap: 20px;
            margin-bottom: 30px;
        }
        
        .metric-card {
            background: #171a23;
            padding: 25px;
            border-radius: 8px;
            border: 1px solid #1e222e;
            transition: transform 0.2s;
        }
        
        .metric-card:hover {
            transform: translateY(-2px);
        }
        
        .metric-label {
            font-size: 14px;
            color: #a1a1aa;
            margin-bottom: 10px;
        }
        
        .metric-value {
            font-size: 36px;
            font-weight: 700;
            margin-bottom: 8px;
        }
        
        .metric-threshold {
            font-size: 13px;
            color: #71717a;
        }
        
        .status-icon {
            display: inline-block;
            width: 12px;
            height: 12px;
            border-radius: 50%;
            margin-left: 8px;
        }
        
        .healthy { color: #22c55e; }
        .healthy-bg { background: #22c55e; }
        .warning { color: #f59e0b; }
        .warning-bg { background: #f59e0b; }
        .critical { color: #ef4444; }
        .critical-bg { background: #ef4444; }
        
        .details {
            background: #171a23;
            padding: 25px;
            border-radius: 8px;
            border: 1px solid #1e222e;
        }
        
        .details h3 {
            font-size: 18px;
            margin-bottom: 15px;
            font-weight: 500;
        }
        
        .details-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 15px;
        }
        
        .detail-item {
            display: flex;
            justify-content: space-between;
            padding: 10px 0;
            border-bottom: 1px solid #1e222e;
        }
        
        .detail-label {
            color: #a1a1aa;
        }
        
        .detail-value {
            font-weight: 600;
        }
        
        .loading {
            text-align: center;
            padding: 60px;
            color: #a1a1aa;
        }
        
        .error {
            background: #7f1d1d;
            color: #fecaca;
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 20px;
            border: 1px solid #991b1b;
        }
        
        @media (max-width: 768px) {
            .metrics-grid {
                grid-template-columns: 1fr;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <header>
            <h1>mosdns-x 监控面板</h1>
            <div class="last-update" id="lastUpdate">加载中...</div>
        </header>
        
        <div id="error" class="error" style="display: none;"></div>
        
        <div id="loading" class="loading">
            <div>正在加载监控数据...</div>
        </div>
        
        <div id="content" style="display: none;">
            <!-- 整体健康评分 -->
            <div class="overall-score">
                <h2>整体健康评分</h2>
                <div class="score-value" id="overallScore">--</div>
                <div class="score-bar">
                    <div class="score-fill" id="scoreFill"></div>
                </div>
            </div>
            
            <!-- 核心指标 -->
            <div class="metrics-grid">
                <div class="metric-card">
                    <div class="metric-label">会话清理失败率</div>
                    <div class="metric-value" id="sessionCleanup">--</div>
                    <div class="metric-threshold">
                        健康: < <span id="sessionCleanupThreshold">--</span>%
                        <span class="status-icon" id="sessionCleanupStatus"></span>
                    </div>
                </div>
                
                <div class="metric-card">
                    <div class="metric-label">数据库连接使用率</div>
                    <div class="metric-value" id="dbConnections">--</div>
                    <div class="metric-threshold">
                        健康: < <span id="dbConnectionsThreshold">--</span>%
                        <span class="status-icon" id="dbConnectionsStatus"></span>
                    </div>
                </div>
                
                <div class="metric-card">
                    <div class="metric-label">限速器内存条目数</div>
                    <div class="metric-value" id="rateLimiter">--</div>
                    <div class="metric-threshold">
                        健康: < <span id="rateLimiterThreshold">--</span>
                        <span class="status-icon" id="rateLimiterStatus"></span>
                    </div>
                </div>
                
                <div class="metric-card">
                    <div class="metric-label">凭证计数不匹配</div>
                    <div class="metric-value" id="credentialCount">--</div>
                    <div class="metric-threshold">
                        健康: <span id="credentialCountThreshold">0</span>
                        <span class="status-icon" id="credentialCountStatus"></span>
                    </div>
                </div>
            </div>
            
            <!-- 详细信息 -->
            <div class="details">
                <h3>详细信息</h3>
                <div class="details-grid">
                    <div class="detail-item">
                        <span class="detail-label">活动会话</span>
                        <span class="detail-value" id="activeSessions">--</span>
                    </div>
                    <div class="detail-item">
                        <span class="detail-label">数据库连接</span>
                        <span class="detail-value" id="dbConnectionsDetail">--</span>
                    </div>
                    <div class="detail-item">
                        <span class="detail-label">运行时间</span>
                        <span class="detail-value" id="uptime">--</span>
                    </div>
                    <div class="detail-item">
                        <span class="detail-label">服务状态</span>
                        <span class="detail-value" id="serviceStatus">--</span>
                    </div>
                </div>
            </div>
        </div>
    </div>
    
    <script>
        let updateInterval;
        
        // 格式化时间
        function formatTime(date) {
            const now = new Date();
            const diff = Math.floor((now - date) / 1000);
            
            if (diff < 10) return '刚刚';
            if (diff < 60) return `${diff} 秒前`;
            if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
            return `${Math.floor(diff / 3600)} 小时前`;
        }
        
        // 更新状态图标
        function updateStatusIcon(elementId, status) {
            const icon = document.getElementById(elementId);
            icon.className = 'status-icon ' + status + '-bg';
        }
        
        // 更新评分条颜色
        function getScoreColor(score) {
            if (score >= 90) return '#22c55e';
            if (score >= 70) return '#f59e0b';
            return '#ef4444';
        }
        
        // 获取并更新数据
        async function fetchData() {
            try {
                const response = await fetch('/health-data');
                if (!response.ok) throw new Error('获取数据失败');
                
                const data = await response.json();
                
                // 隐藏加载和错误
                document.getElementById('loading').style.display = 'none';
                document.getElementById('error').style.display = 'none';
                document.getElementById('content').style.display = 'block';
                
                // 更新整体评分
                const score = data.overall_score;
                document.getElementById('overallScore').textContent = score + '/100';
                const scoreFill = document.getElementById('scoreFill');
                scoreFill.style.width = score + '%';
                scoreFill.style.background = getScoreColor(score);
                
                // 更新会话清理
                document.getElementById('sessionCleanup').textContent = 
                    data.session_cleanup.value.toFixed(1) + data.session_cleanup.unit;
                document.getElementById('sessionCleanupThreshold').textContent = 
                    data.session_cleanup.threshold;
                updateStatusIcon('sessionCleanupStatus', data.session_cleanup.status);
                
                // 更新数据库连接
                document.getElementById('dbConnections').textContent = 
                    data.db_connections.value.toFixed(0) + data.db_connections.unit;
                document.getElementById('dbConnectionsThreshold').textContent = 
                    data.db_connections.threshold;
                updateStatusIcon('dbConnectionsStatus', data.db_connections.status);
                
                // 更新限速器内存
                document.getElementById('rateLimiter').textContent = 
                    data.rate_limiter_memory.value.toFixed(0).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
                document.getElementById('rateLimiterThreshold').textContent = 
                    data.rate_limiter_memory.threshold.toFixed(0).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
                updateStatusIcon('rateLimiterStatus', data.rate_limiter_memory.status);
                
                // 更新凭证计数
                document.getElementById('credentialCount').textContent = 
                    data.credential_count.value.toFixed(0);
                updateStatusIcon('credentialCountStatus', data.credential_count.status);
                
                // 更新详细信息
                document.getElementById('activeSessions').textContent = data.details.active_sessions;
                document.getElementById('dbConnectionsDetail').textContent = 
                    `${data.details.db_connections_in_use} / ${data.details.db_connections_open}`;
                document.getElementById('uptime').textContent = data.details.uptime;
                document.getElementById('serviceStatus').textContent = data.details.service_status;
                
                // 更新时间
                document.getElementById('lastUpdate').textContent = 
                    '最后更新: ' + formatTime(new Date(data.timestamp));
                
            } catch (error) {
                console.error('Error fetching data:', error);
                document.getElementById('loading').style.display = 'none';
                document.getElementById('content').style.display = 'none';
                const errorDiv = document.getElementById('error');
                errorDiv.textContent = '获取监控数据失败: ' + error.message;
                errorDiv.style.display = 'block';
            }
        }
        
        // 启动自动刷新
        function startAutoRefresh() {
            fetchData(); // 立即获取一次
            updateInterval = setInterval(fetchData, 5000); // 每5秒刷新
        }
        
        // 页面加载时启动
        document.addEventListener('DOMContentLoaded', startAutoRefresh);
        
        // 页面卸载时清理
        window.addEventListener('beforeunload', () => {
            if (updateInterval) clearInterval(updateInterval);
        });
    </script>
</body>
</html>
```

---

### 步骤4: 注册路由

在 `internal/controlapi/handler.go` 中添加路由（假设你已经有路由注册的地方）：

```go
// 在路由注册函数中添加
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
    // ... 其他路由 ...
    
    // 监控 Web UI
    mux.HandleFunc("/health-ui", h.ServeHealthUI)      // HTML 页面
    mux.HandleFunc("/health-data", h.ServeHealthData)  // JSON API
}
```

---

### 步骤5: 在 Control Store 中添加健康状态方法

在 `internal/control/store.go` (BoltDB) 或 `mysql_store.go` (MySQL) 中添加：

```go
// HealthStatus 包含所有健康检查指标
type HealthStatus struct {
	Timestamp                time.Time
	ActiveSessions           int
	DBConnectionsInUse       int
	DBConnectionsOpen        int
	SessionCleanupErrors     float64  // 百分比
	RateLimiterEntries       int
	CredentialCountMismatches int
	Uptime                   time.Duration
}

// GetHealthStatus 返回当前健康状态
func (s *Store) GetHealthStatus(ctx context.Context) (HealthStatus, error) {
	// 这里复用方案B中的健康检查逻辑
	// 具体实现参考 MONITORING_QUICKSTART.md
	
	status := HealthStatus{
		Timestamp: time.Now(),
		Uptime:    time.Since(s.startTime),
	}
	
	// 获取活动会话数
	// 获取限速器条目数
	// 计算会话清理失败率
	// ... 等等
	
	return status, nil
}
```

---

### 步骤6: 编译和测试

```bash
# 编译
go build -o mosdns main.go

# 启动服务
./mosdns start -c config.yaml

# 在浏览器打开
open http://localhost:8080/health-ui

# 或者使用 curl 测试 JSON API
curl http://localhost:8080/health-data | jq
```

---

## 📱 使用方式

### 桌面浏览器
```bash
# 访问监控面板
http://localhost:8080/health-ui
```

### 移动设备
```bash
# 如果是本地网络
http://你的服务器IP:8080/health-ui
```

### JSON API（用于集成）
```bash
# 获取原始数据
curl http://localhost:8080/health-data

# 返回示例:
{
  "timestamp": "2026-09-14T15:30:00Z",
  "overall_score": 95,
  "session_cleanup": {
    "value": 0.0,
    "threshold": 1.0,
    "status": "healthy",
    "unit": "%",
    "label": "会话清理失败率"
  },
  "db_connections": {
    "value": 65.0,
    "threshold": 80.0,
    "status": "healthy",
    "unit": "%",
    "label": "数据库连接使用率"
  },
  ...
}
```

---

## ⚙️ 自定义配置

### 修改刷新间隔

编辑 `health.html` 中的 JavaScript：

```javascript
// 默认 5000 毫秒（5秒）
updateInterval = setInterval(fetchData, 5000);

// 改为 10 秒
updateInterval = setInterval(fetchData, 10000);
```

### 修改阈值

编辑 `health_ui.go` 中的阈值：

```go
SessionCleanup: Metric{
    Value:     health.SessionCleanupErrors,
    Threshold: 1.0,  // 修改这里
    // ...
},
```

### 添加新指标

1. 在 `HealthUIResponse` 中添加字段
2. 在 `ServeHealthData` 中填充数据
3. 在 `health.html` 中添加显示元素
4. 更新 JavaScript 填充逻辑

---

## 🔒 安全考虑

### 1. 访问控制（推荐）

只允许本地访问：

```go
func (h *Handler) ServeHealthUI(w http.ResponseWriter, r *http.Request) {
    // 检查是否本地请求
    host := r.RemoteAddr
    if !strings.HasPrefix(host, "127.0.0.1:") && 
       !strings.HasPrefix(host, "[::1]:") {
        http.Error(w, "Forbidden", http.StatusForbidden)
        return
    }
    
    // ... 继续处理
}
```

### 2. 添加基础认证

```go
func (h *Handler) ServeHealthUI(w http.ResponseWriter, r *http.Request) {
    // 简单的基础认证
    user, pass, ok := r.BasicAuth()
    if !ok || user != "admin" || pass != "监控密码" {
        w.Header().Set("WWW-Authenticate", `Basic realm="Health UI"`)
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }
    
    // ... 继续处理
}
```

访问时浏览器会提示输入用户名和密码。

### 3. 使用 HTTPS

在生产环境建议配置 TLS：

```yaml
# config.yaml
control:
  tls:
    enabled: true
    cert: /path/to/cert.pem
    key: /path/to/key.pem
```

---

## 📊 方案对比更新

| 特性 | 方案D<br/>Web UI | 方案B<br/>日志 | 方案A<br/>Prometheus |
|------|----------------|-------------|------------------|
| **实施时间** | 半天（4小时） | 1-2小时 | 2-4天 |
| **查看方式** | ✅ 浏览器 | ❌ 命令行 | ✅ Grafana |
| **额外设施** | 无需 | 无需 | 需要 |
| **实时性** | 5秒 | 60秒 | 15秒 |
| **移动访问** | ✅ 响应式 | ❌ | ✅ |
| **历史数据** | ❌ | ✅ 日志 | ✅ 长期 |
| **自动告警** | ❌ | ❌ | ✅ |
| **复杂度** | ⭐⭐ | ⭐ | ⭐⭐⭐ |
| **推荐场景** | 想要 Web 界面<br/>不想装 Grafana | 快速简单 | 企业级生产 |

---

## ✅ 优缺点

### 优点

- ✅ 无需 Grafana 等额外工具
- ✅ 直接在浏览器查看，美观直观
- ✅ 自动刷新，实时监控
- ✅ 响应式设计，支持移动设备
- ✅ 单文件 HTML，易于定制
- ✅ JSON API 可用于集成

### 缺点

- ❌ 不保存历史数据（仅当前状态）
- ❌ 无自动告警功能
- ❌ 无图表可视化（仅数字显示）
- ❌ 需要修改源代码并重新编译

---

## 🚀 快速验证

完成实施后，验证功能：

```bash
# 1. 检查 HTML 页面是否可访问
curl -I http://localhost:8080/health-ui

# 2. 检查 JSON API
curl http://localhost:8080/health-data | jq .overall_score

# 3. 在浏览器打开
open http://localhost:8080/health-ui

# 预期: 看到漂亮的监控面板，每5秒自动刷新
```

---

## 💡 进一步增强（可选）

### 1. 添加简单图表

使用 Chart.js CDN（无需安装）：

```html
<!-- 在 </body> 前添加 -->
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
<canvas id="metricsChart" width="400" height="200"></canvas>
<script>
// 创建简单的折线图显示历史趋势
</script>
```

### 2. 导出功能

添加导出按钮：

```javascript
function exportData() {
    const data = {/* 当前数据 */};
    const blob = new Blob([JSON.stringify(data, null, 2)], 
        {type: 'application/json'});
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'health-' + new Date().toISOString() + '.json';
    a.click();
}
```

### 3. 历史记录（客户端）

使用 localStorage 保存最近的数据点：

```javascript
function saveHistory(data) {
    let history = JSON.parse(localStorage.getItem('healthHistory') || '[]');
    history.push({timestamp: new Date(), score: data.overall_score});
    // 只保留最近 100 条
    if (history.length > 100) history = history.slice(-100);
    localStorage.setItem('healthHistory', JSON.stringify(history));
}
```

---

## 📞 故障排查

### 问题1: 页面显示404

**原因**: 路由未注册或 embed 文件路径错误

**解决**:
```go
// 检查 go:embed 指令路径
//go:embed templates/health.html
var healthUIFS embed.FS

// 确保文件存在于正确位置
// internal/controlapi/templates/health.html
```

### 问题2: JSON API 返回错误

**原因**: 健康检查方法未实现

**解决**:
参考 [MONITORING_QUICKSTART.md](MONITORING_QUICKSTART.md) 实施方案B的健康检查代码。

### 问题3: 数据不刷新

**原因**: CORS 或网络问题

**解决**:
```go
// 添加 CORS 头
w.Header().Set("Access-Control-Allow-Origin", "*")
w.Header().Set("Access-Control-Allow-Methods", "GET")
```

---

## 🎉 总结

方案D提供了一个**完美的中间地带**：

- 比方案B（日志）更直观 - 有漂亮的 Web 界面
- 比方案A（Prometheus）更简单 - 无需额外基础设施
- 实施时间适中 - 半天就能完成
- 维护成本低 - 无需管理额外服务

**推荐给**: 想要 Web 监控但不想安装 Grafana 的用户

---

**实施时间**: 4小时（假设已有方案B的健康检查代码）  
**难度**: ⭐⭐ (中等)  
**维护成本**: 极低  
**用户体验**: ⭐⭐⭐⭐⭐ (优秀)

下一步: 开始实施或查看 [MONITORING_QUICKSTART.md](MONITORING_QUICKSTART.md)！
