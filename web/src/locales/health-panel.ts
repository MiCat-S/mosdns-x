// Translations for the Chinese message keys used in health-panel.tsx.
export const en: Record<string, string> = {
  正常: "Healthy",
  警告: "Warning",
  异常: "Critical",
  数据不足: "Insufficient data",
  不适用: "Not applicable",
  暂无样本: "No samples yet",
  历史参考: "Historical reference",
  "尚无会话清理样本，不计算失败率，也不影响当前评分":
    "No session cleanup samples yet, so no failure rate is calculated and the current score is unaffected",
  "进程累计值，仅作历史参考；近期告警使用窗口增量":
    "Cumulative since the process started, for historical reference only; recent alerts use the increase within the window",
  "bbolt 不使用 MySQL 事务或连接池":
    "bbolt doesn't use MySQL transactions or a connection pool",
  "MySQL 按事务查询计数，没有独立计数缓存":
    "MySQL counts with transactional queries and keeps no separate count cache",
  "连接池未设置上限，无法计算占用比例":
    "The connection pool has no limit, so its usage can't be calculated",
  等待首次数据库采集: "Waiting for the first database collection",
  "数据库采集失败，请检查服务日志":
    "Database collection failed. Check the service logs",
  "数据量超过扫描预算；可调整 control.health_scan_limit，运行统计仍独立提供":
    "The data exceeds the scan budget; you can adjust control.health_scan_limit. Runtime statistics are still provided separately",
  "数据库快照已过期，等待重新采集":
    "The database snapshot is stale; waiting for the next collection",
  当前后端未提供健康检查: "The current backend doesn't provide health checks",
  采集值无效: "Invalid collected value",
  无法读取有效容量: "Can't read a valid capacity",
  运行统计不可用: "Runtime statistics unavailable",
  累计会话清理失败率: "Cumulative session cleanup failure rate",
  "IP 限速器最高占用": "Peak IP rate limiter usage",
  控制库连接池占用: "Control DB connection pool usage",
  凭证一致性异常: "Credential consistency issues",
  "累计 MySQL 回滚失败": "Cumulative MySQL rollback failures",
  "{value}处": "{value}",
  "{value}次": "{value}",
  监控数据读取失败: "Failed to load monitoring data",
  安全与运行监控: "Security and runtime monitoring",
  "页面每 5 秒刷新；数据库每分钟扫描。连接池与限速器读取当前内存统计。":
    "The page refreshes every 5 seconds and the database is scanned every minute. Connection pool and rate limiter figures come from current in-memory statistics.",
  "读取中…": "Loading…",
  立即刷新: "Refresh now",
  "自动刷新：开": "Auto refresh: on",
  "自动刷新：关": "Auto refresh: off",
  "已观测项评分：{score} / 100": "Score for observed metrics: {score} / 100",
  "已观测项评分：{score}（指标不足）":
    "Score for observed metrics: {score} (not enough metrics)",
  "更新：{time}": "Updated: {time}",
  "数据库采集：{time}": "Database collected: {time}",
  "数据库采集：尚未采集": "Database collected: not yet",
  "运行 {minutes} 分钟": "Up {minutes} min",
  数据库状态不可用: "Database status unavailable",
  "安全监控指标表格（可横向滚动）":
    "Security metrics table (scrolls horizontally)",
  安全监控指标: "Security metrics",
  指标: "Metric",
  观测值: "Observed value",
  状态与说明: "Status and notes",
  指标不可用: "Metric unavailable",
  "清理失败率、回滚失败与拒绝计数从进程启动累计，仅作历史参考，不影响当前评分。":
    "Cleanup failure rate, rollback failures and rejection counts accumulate from process start; they are for historical reference only and don't affect the current score.",
  "暂无样本不等于采集失败；未知的适用指标仍会使评分不可用。评分不代表安全审计结论或服务可用性承诺。":
    '"No samples yet" doesn\'t mean collection failed; an applicable metric that is unknown still makes the score unavailable. The score is not a security audit finding or a service availability commitment.',
  有效面板会话: "Active panel sessions",
  "会话清理尝试 / 失败": "Session cleanup attempts / failures",
  "控制库使用中 / 连接上限": "Control DB connections in use / limit",
  无限制: "Unlimited",
  "累计等待 {count} 次": "Waited {count} times in total",
  "bbolt 只读事务 / 待复用页": "bbolt read transactions / pending pages",
  "IP 限速器表格（可横向滚动）": "IP rate limiter table (scrolls horizontally)",
  "IP 限速器": "IP rate limiters",
  限速器: "Rate limiter",
  "有效条目 / 容量": "Active entries / capacity",
  内存保留条目: "Entries in memory",
  累计拒绝: "Total rejected",
  累计清理: "Total evicted",
  面板登录: "Panel sign-in",
  "面板 DNS 查询": "Panel DNS queries",
  "暂无监控数据。": "No monitoring data yet.",
};

export const ja: Record<string, string> = {
  正常: "正常",
  警告: "警告",
  异常: "異常",
  数据不足: "データ不足",
  不适用: "該当なし",
  暂无样本: "サンプルなし",
  历史参考: "参考値（累計）",
  "尚无会话清理样本，不计算失败率，也不影响当前评分":
    "セッションクリーンアップのサンプルがまだないため、失敗率は計算せず、現在のスコアにも影響しません",
  "进程累计值，仅作历史参考；近期告警使用窗口增量":
    "プロセス起動以降の累計値で、参考としてのみ表示します。直近のアラートは期間内の増分で判定します",
  "bbolt 不使用 MySQL 事务或连接池":
    "bbolt は MySQL のトランザクションや接続プールを使用しません",
  "MySQL 按事务查询计数，没有独立计数缓存":
    "MySQL はトランザクション内のクエリで件数を数えるため、個別のカウントキャッシュはありません",
  "连接池未设置上限，无法计算占用比例":
    "接続プールに上限が設定されていないため、使用率を計算できません",
  等待首次数据库采集: "初回のデータベース収集を待っています",
  "数据库采集失败，请检查服务日志":
    "データベースの収集に失敗しました。サービスログを確認してください",
  "数据量超过扫描预算；可调整 control.health_scan_limit，运行统计仍独立提供":
    "データ量がスキャン上限を超えています。control.health_scan_limit で調整できます。実行統計は引き続き個別に提供されます",
  "数据库快照已过期，等待重新采集":
    "データベースのスナップショットが古くなっています。再収集を待っています",
  当前后端未提供健康检查:
    "現在のバックエンドはヘルスチェックを提供していません",
  采集值无效: "収集値が無効です",
  无法读取有效容量: "有効な容量を読み取れません",
  运行统计不可用: "実行統計を利用できません",
  累计会话清理失败率: "セッションクリーンアップ失敗率（累計）",
  "IP 限速器最高占用": "IP レート制限の最大使用率",
  控制库连接池占用: "制御 DB の接続プール使用率",
  凭证一致性异常: "認証情報の不整合",
  "累计 MySQL 回滚失败": "MySQL ロールバック失敗（累計）",
  "{value}处": "{value} 件",
  "{value}次": "{value} 回",
  监控数据读取失败: "監視データの読み込みに失敗しました",
  安全与运行监控: "セキュリティと稼働状況の監視",
  "页面每 5 秒刷新；数据库每分钟扫描。连接池与限速器读取当前内存统计。":
    "ページは 5 秒ごとに更新され、データベースは 1 分ごとにスキャンされます。接続プールとレート制限は現在のメモリ上の統計を表示します。",
  "读取中…": "読み込み中…",
  立即刷新: "今すぐ更新",
  "自动刷新：开": "自動更新：オン",
  "自动刷新：关": "自動更新：オフ",
  "已观测项评分：{score} / 100": "観測項目のスコア：{score} / 100",
  "已观测项评分：{score}（指标不足）": "観測項目のスコア：{score}（指標不足）",
  "更新：{time}": "更新：{time}",
  "数据库采集：{time}": "データベース収集：{time}",
  "数据库采集：尚未采集": "データベース収集：未実施",
  "运行 {minutes} 分钟": "稼働 {minutes} 分",
  数据库状态不可用: "データベースの状態を取得できません",
  "安全监控指标表格（可横向滚动）":
    "セキュリティ監視指標の表（横スクロール可）",
  安全监控指标: "セキュリティ監視指標",
  指标: "指標",
  观测值: "観測値",
  状态与说明: "状態と説明",
  指标不可用: "指標を利用できません",
  "清理失败率、回滚失败与拒绝计数从进程启动累计，仅作历史参考，不影响当前评分。":
    "クリーンアップ失敗率、ロールバック失敗、拒否数はプロセス起動からの累計で、参考としてのみ表示し、現在のスコアには影響しません。",
  "暂无样本不等于采集失败；未知的适用指标仍会使评分不可用。评分不代表安全审计结论或服务可用性承诺。":
    "「サンプルなし」は収集の失敗を意味しません。該当する指標が不明な場合、スコアは算出されません。スコアはセキュリティ監査の結論やサービス可用性の保証を示すものではありません。",
  有效面板会话: "有効なパネルセッション",
  "会话清理尝试 / 失败": "セッションクリーンアップ試行 / 失敗",
  "控制库使用中 / 连接上限": "制御 DB 使用中 / 接続上限",
  无限制: "無制限",
  "累计等待 {count} 次": "累計待機 {count} 回",
  "bbolt 只读事务 / 待复用页":
    "bbolt 読み取りトランザクション / 再利用待ちページ",
  "IP 限速器表格（可横向滚动）": "IP レート制限の表（横スクロール可）",
  "IP 限速器": "IP レート制限",
  限速器: "レート制限",
  "有效条目 / 容量": "有効エントリ / 容量",
  内存保留条目: "メモリ保持エントリ",
  累计拒绝: "累計拒否",
  累计清理: "累計削除",
  面板登录: "パネルログイン",
  "面板 DNS 查询": "パネル DNS クエリ",
  "暂无监控数据。": "監視データはまだありません。",
};
