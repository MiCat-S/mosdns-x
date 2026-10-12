import { useSyncExternalStore } from "react";
import { en, ja } from "./locales";

// The panel is written in Simplified Chinese and the Chinese text is the
// message key (gettext style): t("用户") looks the string up in the English
// or Japanese table and falls back to the Chinese source when an entry is
// missing. Every literal passed to t() or msg() must have an entry in both
// tables; i18n.test.ts checks this.

export type Lang = "zh" | "en" | "ja";

export const languages: {
  code: Lang;
  label: string;
  locale: string;
  htmlLang: string;
}[] = [
  { code: "zh", label: "简体中文", locale: "zh-CN", htmlLang: "zh-Hans" }, // i18n-ignore
  { code: "en", label: "English", locale: "en-US", htmlLang: "en" },
  { code: "ja", label: "日本語", locale: "ja-JP", htmlLang: "ja" }, // i18n-ignore
];

const storageKey = "mosdns.lang";
const tables: Record<Lang, Record<string, string> | undefined> = {
  zh: undefined,
  en,
  ja,
};

function isLang(value: unknown): value is Lang {
  return value === "zh" || value === "en" || value === "ja";
}

// A saved choice wins; otherwise the first browser language we support,
// and English for everyone else.
export function detectLang(
  saved: string | null,
  preferred: readonly string[],
): Lang {
  if (isLang(saved)) return saved;
  for (const tag of preferred) {
    const base = tag.toLowerCase().split("-")[0];
    if (isLang(base)) return base;
  }
  return "en";
}

function readSaved() {
  try {
    return localStorage.getItem(storageKey);
  } catch {
    return null;
  }
}

let current: Lang = detectLang(
  readSaved(),
  typeof navigator === "undefined"
    ? []
    : navigator.languages?.length
      ? navigator.languages
      : [navigator.language],
);
const listeners = new Set<() => void>();

export function lang() {
  return current;
}

// BCP 47 locale for Intl date and number formatting.
export function locale() {
  return languages.find((l) => l.code === current)!.locale;
}

// Intl formatters are costly to build and tables format thousands of
// values, so one formatter is kept per locale and set of options.
const numberFormats = new Map<string, Intl.NumberFormat>();
const dateFormats = new Map<string, Intl.DateTimeFormat>();

function cached<F, O>(
  cache: Map<string, F>,
  make: (tag: string, options?: O) => F,
  tag: string,
  options?: O,
) {
  const key = options ? `${tag} ${JSON.stringify(options)}` : tag;
  let formatter = cache.get(key);
  if (!formatter) {
    formatter = make(tag, options);
    cache.set(key, formatter);
  }
  return formatter;
}

export function numberFormat(
  options?: Intl.NumberFormatOptions,
  tag = locale(),
) {
  return cached(
    numberFormats,
    (l, o?: Intl.NumberFormatOptions) => new Intl.NumberFormat(l, o),
    tag,
    options,
  );
}

export function dateFormat(
  options?: Intl.DateTimeFormatOptions,
  tag = locale(),
) {
  return cached(
    dateFormats,
    (l, o?: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat(l, o),
    tag,
    options,
  );
}

export function t(source: string, params?: Record<string, string | number>) {
  const text = tables[current]?.[source] ?? source;
  if (!params) return text;
  return text.replace(/\{(\w+)\}/g, (match, key: string) =>
    key in params ? String(params[key]) : match,
  );
}

// Marks a literal for translation where it is stored (navigation tables,
// label maps) and translated later with t(value).
export function msg(source: string) {
  return source;
}

function applyDocument() {
  if (typeof document === "undefined") return;
  document.documentElement.lang = languages.find(
    (l) => l.code === current,
  )!.htmlLang;
  document.title = t("MosDNS 服务中心");
}

export function setLang(next: Lang) {
  current = next;
  try {
    localStorage.setItem(storageKey, next);
  } catch {
    /* private mode: the choice lasts for this page only */
  }
  applyDocument();
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useLang() {
  return useSyncExternalStore(subscribe, lang, lang);
}

// Text translated under one language and kept in state, such as the error
// of a failed request, is mapped back to its source so it can be shown in
// the current language. Text with parameters filled in cannot be mapped
// and is shown as it was.
let sources: Map<string, string> | undefined;
export function retranslate(text: string) {
  if (!sources) {
    sources = new Map();
    for (const table of Object.values(tables))
      for (const [source, value] of Object.entries(table ?? {}))
        sources.set(value, source);
  }
  return t(sources.get(text) ?? text);
}

applyDocument();
