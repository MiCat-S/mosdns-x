// Translations for the Chinese message keys used in top-domains.tsx.
export const en: Record<string, string> = {
  查询最多的域名: "Top domains",
  "查询明细已关闭，无法统计查询最多的域名。":
    "Query logging is off, so domains can't be ranked.",
  "查询量最多的 {n} 个域名，统计 {from} 至 {to} 的 {count} 次查询":
    "The {n} most queried domains among {count} queries from {from} to {to}",
  "占 {share} · 缓存命中 {hits}": "{share} of queries · {hits} cache hits",
};

export const ja: Record<string, string> = {
  查询最多的域名: "クエリ数上位のドメイン",
  "查询明细已关闭，无法统计查询最多的域名。":
    "クエリログが無効のため、ドメインを集計できません。",
  "查询量最多的 {n} 个域名，统计 {from} 至 {to} 的 {count} 次查询":
    "{from} ～ {to} の {count} 件のクエリのうち、クエリ数上位 {n} 件のドメイン",
  "占 {share} · 缓存命中 {hits}": "全体の {share} · キャッシュヒット {hits}",
};
