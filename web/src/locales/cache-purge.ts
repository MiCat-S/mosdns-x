// Translations for the Chinese message keys used in cache-purge.tsx.
export const en: Record<string, string> = {
  "请输入有效的域名，例如 example.com。":
    "Enter a valid domain, e.g. example.com.",
  "不需要输入 *，勾选“同时清除子域名”即可。":
    'Leave out the *; check "Also clear subdomains" instead.',
  "当前运行配置中没有缓存插件。":
    "The running configuration has no cache plugin.",
  "缓存中没有 {domain} 及其子域名的记录。":
    "Nothing is cached for {domain} or its subdomains.",
  "缓存中没有 {domain} 的记录。": "Nothing is cached for {domain}.",
  "已清除 {domain} 及其子域名的 {count} 条缓存记录。":
    "Cache entries removed for {domain} and its subdomains: {count}.",
  "已清除 {domain} 的 {count} 条缓存记录。":
    "Cache entries removed for {domain}: {count}.",
  清除域名缓存: "Clear domain cache",
  "删除该域名在所有缓存插件中的记录（全部查询类型），下一次查询会重新向上游请求。":
    "Removes the domain's records from every cache plugin, for all query types. The next query goes to the upstream again.",
  域名: "Domain",
  "正在清除…": "Clearing…",
  清除缓存: "Clear cache",
  同时清除子域名: "Also clear subdomains",
  清除此域名缓存: "Clear this domain from cache",
};

export const ja: Record<string, string> = {
  "请输入有效的域名，例如 example.com。":
    "有効なドメインを入力してください（例：example.com）。",
  "不需要输入 *，勾选“同时清除子域名”即可。":
    "* は不要です。「サブドメインも消去」にチェックを入れてください。",
  "当前运行配置中没有缓存插件。":
    "実行中の設定にキャッシュプラグインがありません。",
  "缓存中没有 {domain} 及其子域名的记录。":
    "{domain} とそのサブドメインのキャッシュはありません。",
  "缓存中没有 {domain} 的记录。": "{domain} のキャッシュはありません。",
  "已清除 {domain} 及其子域名的 {count} 条缓存记录。":
    "{domain} とそのサブドメインのキャッシュを {count} 件消去しました。",
  "已清除 {domain} 的 {count} 条缓存记录。":
    "{domain} のキャッシュを {count} 件消去しました。",
  清除域名缓存: "ドメインのキャッシュを消去",
  "删除该域名在所有缓存插件中的记录（全部查询类型），下一次查询会重新向上游请求。":
    "すべてのキャッシュプラグインから、このドメインのレコードを全クエリタイプ分削除します。次回のクエリはアップストリームに改めて問い合わせます。",
  域名: "ドメイン",
  "正在清除…": "消去中…",
  清除缓存: "キャッシュを消去",
  同时清除子域名: "サブドメインも消去",
  清除此域名缓存: "このドメインのキャッシュを消去",
};
