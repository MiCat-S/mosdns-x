// Translations for the Chinese message keys used in query-trace.tsx.
export const en: Record<string, string> = {
  主要: "Primary",
  备援: "Fallback",
  后台刷新: "Background refresh",
  参考查询: "Reference query",
  "并行 {n}": "Parallel {n}",
  超时: "Timed out",
  已取消: "Canceled",
  "TLS 错误": "TLS error",
  连接被拒绝: "Connection refused",
  连接中断: "Connection reset",
  无法连接: "Connection failed",
  无法解析上游地址: "Couldn't resolve the upstream address",
  "HTTP 错误": "HTTP error",
  响应无效: "Invalid response",
  空响应: "Empty response",
  其他错误: "Other error",
  "未完成（已先返回其他结果）":
    "Unfinished (another result was returned first)",
  无响应码: "No response code",
  "主要上游失败，启用备援": "Primary upstream failed; fallback started",
  "主要上游未及时回应，同时启用备援":
    "Primary upstream didn't respond in time; fallback started alongside it",
  备援始终同时查询: "Fallback is always queried in parallel",
  "命中缓存 {plugin}": "Cache hit in {plugin}",
  "缓存已过期，先返回旧结果，并在后台刷新 {plugin}":
    "Cache expired; returned the stale result first and refreshing {plugin} in the background",
  启用备援: "Fallback started",
  "采用{branch}的结果": "Used the result from {branch}",
  采用该分支的结果: "Used the result from this branch",
  "主要上游近期失败较多，主要与备援同时查询":
    "Primary upstream has failed often recently; querying primary and fallback in parallel",
  "负载均衡选择第 {group} 组": "Load balancer chose group {group}",
  "查询多且结果未变，TTL 已延长至 {ttl} 秒":
    "Queried often with an unchanged answer, so TTL lengthened to {ttl} s",
  "缓存 · 原始 {upstream}": "Cache · originally {upstream}",
  "缓存（来源未记录）": "Cache (source not recorded)",
  "本地 Hosts": "Local Hosts",
  本地处理: "Handled locally",
  上游全部失败: "All upstreams failed",
  未发出上游请求: "No upstream request sent",
  未记录: "Not recorded",
  "、": ", ",
  未命中分流规则: "No routing rule matched",
  "命中 {rule}": "Matched {rule}",
  "命中 {rule}，{count} 次上游请求均未取得可用结果":
    "Matched {rule}; {count} upstream requests returned no usable result",
  "未命中分流规则，{count} 次上游请求均未取得可用结果":
    "No routing rule matched; {count} upstream requests returned no usable result",
  "命中 {rule}，没有发出上游请求":
    "Matched {rule}; no upstream request was sent",
  "未命中分流规则，没有发出上游请求":
    "No routing rule matched; no upstream request was sent",
  "命中 {rule} → {upstream}，{duration} 取得结果":
    "Matched {rule} → {upstream}, result in {duration}",
  "未命中分流规则 → {upstream}，{duration} 取得结果":
    "No routing rule matched → {upstream}, result in {duration}",
  "命中 {rule} → {upstream}，{duration} 取得结果（主要上游失败，改用备援）":
    "Matched {rule} → {upstream}, result in {duration} (primary upstream failed, switched to fallback)",
  "未命中分流规则 → {upstream}，{duration} 取得结果（主要上游失败，改用备援）":
    "No routing rule matched → {upstream}, result in {duration} (primary upstream failed, switched to fallback)",
  "命中 {rule} → {upstream}，{duration} 取得结果（主要上游未及时回应，改用备援）":
    "Matched {rule} → {upstream}, result in {duration} (primary upstream didn't respond in time, switched to fallback)",
  "未命中分流规则 → {upstream}，{duration} 取得结果（主要上游未及时回应，改用备援）":
    "No routing rule matched → {upstream}, result in {duration} (primary upstream didn't respond in time, switched to fallback)",
  "命中 {rule} → {upstream}，{duration} 取得结果（采用备援的结果）":
    "Matched {rule} → {upstream}, result in {duration} (used the fallback result)",
  "未命中分流规则 → {upstream}，{duration} 取得结果（采用备援的结果）":
    "No routing rule matched → {upstream}, result in {duration} (used the fallback result)",
  "直接命中缓存，原始来源 {upstream}，{duration} 返回":
    "Cache hit, answered in {duration} (originally from {upstream})",
  "直接命中缓存，{duration} 返回": "Cache hit, answered in {duration}",
  或: "or",
  且: "and",
  非: "not",
  命中: "Matched",
  未命中: "Not matched",
  "未检查（前面的条件已决定结果）":
    "Not checked (an earlier condition decided the result)",
  "{matcher}：{state}": "{matcher}: {state}",
  进入此分支: "Enter this branch",
  "进入 else 分支": "Enter else branch",
  继续往下: "Continue",
  是: "Yes",
  否: "No",
  "未命中 {count} 条规则": "{count} rules not matched",
  命中缓存: "Cache hit",
  "原始来源 {upstream}": "Originally from {upstream}",
  原始来源未记录: "Original source not recorded",
  采用: "Used",
  "出站 DNS": "Outbound DNS",
  查询路径: "Query path",
  收到查询: "Query received",
  未知协议: "Unknown protocol",
  "共 {duration}": "Total {duration}",
  返回: "Returned",
  "{count} 个地址": "Addresses: {count}",
  无地址记录: "No address records",
  上游请求时间轴: "Upstream request timeline",
  从第一次请求起计时: "Timed from the first request",
  "第一次请求后 {time}：{event}": "{time} after the first request: {event}",
  "历史记录未采集查询路径。":
    "The query path wasn't recorded for this older entry.",
  图例: "Legend",
  未检查: "Not checked",
  "记录过多，只保留了前面的部分。":
    "Too many steps were recorded; only the first ones were kept.",
};

export const ja: Record<string, string> = {
  主要: "プライマリ",
  备援: "フォールバック",
  后台刷新: "バックグラウンド更新",
  参考查询: "参照クエリ",
  "并行 {n}": "並列 {n}",
  超时: "タイムアウト",
  已取消: "キャンセル済み",
  "TLS 错误": "TLS エラー",
  连接被拒绝: "接続拒否",
  连接中断: "接続中断",
  无法连接: "接続失敗",
  无法解析上游地址: "アップストリームのアドレスを解決できません",
  "HTTP 错误": "HTTP エラー",
  响应无效: "無効な応答",
  空响应: "空の応答",
  其他错误: "その他のエラー",
  "未完成（已先返回其他结果）": "未完了（別の結果を先に返却済み）",
  无响应码: "応答コードなし",
  "主要上游失败，启用备援":
    "プライマリのアップストリームが失敗し、フォールバックを開始",
  "主要上游未及时回应，同时启用备援":
    "プライマリのアップストリームが時間内に応答せず、フォールバックも同時に開始",
  备援始终同时查询: "フォールバックにも常に同時にクエリ",
  "命中缓存 {plugin}": "{plugin} でキャッシュヒット",
  "缓存已过期，先返回旧结果，并在后台刷新 {plugin}":
    "キャッシュが期限切れのため古い結果を先に返し、{plugin} をバックグラウンドで更新",
  启用备援: "フォールバックを開始",
  "采用{branch}的结果": "{branch}の結果を採用",
  采用该分支的结果: "この分岐の結果を採用",
  "主要上游近期失败较多，主要与备援同时查询":
    "プライマリのアップストリームで最近失敗が多いため、プライマリとフォールバックに同時にクエリ",
  "负载均衡选择第 {group} 组": "負荷分散でグループ {group} を選択",
  "查询多且结果未变，TTL 已延长至 {ttl} 秒":
    "クエリが多く結果も同じため、TTL を {ttl} 秒に延長",
  "缓存 · 原始 {upstream}": "キャッシュ · 当初の取得元 {upstream}",
  "缓存（来源未记录）": "キャッシュ（取得元は未記録）",
  "本地 Hosts": "ローカル Hosts",
  本地处理: "ローカル処理",
  上游全部失败: "すべてのアップストリームが失敗",
  未发出上游请求: "アップストリームへのリクエストなし",
  未记录: "未記録",
  "、": "、",
  未命中分流规则: "一致する振り分けルールなし",
  "命中 {rule}": "{rule} に一致",
  "命中 {rule}，{count} 次上游请求均未取得可用结果":
    "{rule} に一致、{count} 件のアップストリームリクエストはいずれも有効な結果なし",
  "未命中分流规则，{count} 次上游请求均未取得可用结果":
    "一致する振り分けルールなし、{count} 件のアップストリームリクエストはいずれも有効な結果なし",
  "命中 {rule}，没有发出上游请求":
    "{rule} に一致、アップストリームへのリクエストなし",
  "未命中分流规则，没有发出上游请求":
    "一致する振り分けルールなし、アップストリームへのリクエストなし",
  "命中 {rule} → {upstream}，{duration} 取得结果":
    "{rule} に一致 → {upstream}、{duration} で結果を取得",
  "未命中分流规则 → {upstream}，{duration} 取得结果":
    "一致する振り分けルールなし → {upstream}、{duration} で結果を取得",
  "命中 {rule} → {upstream}，{duration} 取得结果（主要上游失败，改用备援）":
    "{rule} に一致 → {upstream}、{duration} で結果を取得（プライマリのアップストリームが失敗したためフォールバックに切り替え）",
  "未命中分流规则 → {upstream}，{duration} 取得结果（主要上游失败，改用备援）":
    "一致する振り分けルールなし → {upstream}、{duration} で結果を取得（プライマリのアップストリームが失敗したためフォールバックに切り替え）",
  "命中 {rule} → {upstream}，{duration} 取得结果（主要上游未及时回应，改用备援）":
    "{rule} に一致 → {upstream}、{duration} で結果を取得（プライマリのアップストリームが時間内に応答しなかったためフォールバックに切り替え）",
  "未命中分流规则 → {upstream}，{duration} 取得结果（主要上游未及时回应，改用备援）":
    "一致する振り分けルールなし → {upstream}、{duration} で結果を取得（プライマリのアップストリームが時間内に応答しなかったためフォールバックに切り替え）",
  "命中 {rule} → {upstream}，{duration} 取得结果（采用备援的结果）":
    "{rule} に一致 → {upstream}、{duration} で結果を取得（フォールバックの結果を採用）",
  "未命中分流规则 → {upstream}，{duration} 取得结果（采用备援的结果）":
    "一致する振り分けルールなし → {upstream}、{duration} で結果を取得（フォールバックの結果を採用）",
  "直接命中缓存，原始来源 {upstream}，{duration} 返回":
    "キャッシュヒット、{duration} で応答（当初の取得元：{upstream}）",
  "直接命中缓存，{duration} 返回": "キャッシュヒット、{duration} で応答",
  或: "または",
  且: "かつ",
  非: "非",
  命中: "一致",
  未命中: "不一致",
  "未检查（前面的条件已决定结果）": "未評価（前の条件で結果が確定済み）",
  "{matcher}：{state}": "{matcher}：{state}",
  进入此分支: "この分岐へ進む",
  "进入 else 分支": "else 分岐へ進む",
  继续往下: "次へ進む",
  是: "はい",
  否: "いいえ",
  "未命中 {count} 条规则": "{count} 件のルールに不一致",
  命中缓存: "キャッシュヒット",
  "原始来源 {upstream}": "当初の取得元：{upstream}",
  原始来源未记录: "当初の取得元は未記録",
  采用: "採用",
  "出站 DNS": "送信先 DNS",
  查询路径: "クエリ経路",
  收到查询: "クエリ受信",
  未知协议: "不明なプロトコル",
  "共 {duration}": "合計 {duration}",
  返回: "応答",
  "{count} 个地址": "アドレス {count} 件",
  无地址记录: "アドレスレコードなし",
  上游请求时间轴: "アップストリームリクエストのタイムライン",
  从第一次请求起计时: "最初のリクエストから計測",
  "第一次请求后 {time}：{event}": "最初のリクエストから {time} 後：{event}",
  "历史记录未采集查询路径。":
    "この過去の記録にはクエリ経路が含まれていません。",
  图例: "凡例",
  未检查: "未評価",
  "记录过多，只保留了前面的部分。":
    "記録が多すぎるため、先頭の部分のみ保持しています。",
};
