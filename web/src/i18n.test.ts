import { afterEach, describe, expect, it } from "vitest";
import { detectLang, lang, msg, setLang, t } from "./i18n";
import { en, ja, localeParts } from "./locales";

// Raw text of every panel source file (tests excluded) and locale table.
const raw = (files: Record<string, string>) =>
  Object.entries(files).map(([path, text]) => ({
    name: path.replace(/^\.\//, ""),
    text,
  }));
const sources = raw(
  import.meta.glob<string>(
    ["./*.ts", "./*.tsx", "!./*.test.ts", "!./*.test.tsx", "!./vite-env.d.ts"],
    { query: "?raw", import: "default", eager: true },
  ),
);
const localeTexts = raw(
  import.meta.glob<string>("./locales/*.ts", {
    query: "?raw",
    import: "default",
    eager: true,
  }),
);
const charsetText = Object.values(
  import.meta.glob<string>("./fonts/charset.txt", {
    query: "?raw",
    import: "default",
    eager: true,
  }),
)[0];

// A t("…") or msg("…") call whose first argument is a plain string literal.
const keyCall = /\b(?:t|msg)\(\s*"((?:[^"\\\n]|\\.)*)"/g;
const cjk = /[\u3040-\u30ff\u3400-\u9fff\uff00-\uffef]/;

function keysIn(text: string) {
  return [...text.matchAll(keyCall)].map((m) => JSON.parse(`"${m[1]}"`));
}

const used = new Map<string, string>();
for (const { name, text } of sources)
  for (const key of keysIn(text)) used.set(key, name);

function placeholders(text: string) {
  return [...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
}

afterEach(() => setLang("zh"));

describe("界面文案翻译", () => {
  it("源码里的中文都经过 t() 或 msg()", () => {
    const leaks: string[] = [];
    for (const { name, text } of sources) {
      // Drop the keys first (keeping line breaks), since Prettier may put a
      // long key on the line after "t(".
      const cleaned = text
        .replace(keyCall, (call) => call.replace(/[^\n]/g, ""))
        .split("\n");
      text.split("\n").forEach((line, index) => {
        if (line.includes("i18n-ignore")) return;
        const stripped = cleaned[index]
          .replace(/\/\/.*$/, "")
          .replace(/\/\*.*?\*\//g, "")
          .replace(/^\s*\*.*$/, "");
        if (cjk.test(stripped))
          leaks.push(`${name}:${index + 1}: ${line.trim()}`);
      });
    }
    expect(leaks).toEqual([]);
  });

  it("每个文案都有英文和日文，且没有多余的条目", () => {
    const keys = [...used.keys()];
    expect(keys.filter((key) => !(key in en))).toEqual([]);
    expect(keys.filter((key) => !(key in ja))).toEqual([]);
    expect(Object.keys(en).filter((key) => !used.has(key))).toEqual([]);
    expect(Object.keys(ja).filter((key) => !used.has(key))).toEqual([]);
  });

  it("译文保留原文的占位符", () => {
    const broken: string[] = [];
    for (const [key, value] of [...Object.entries(en), ...Object.entries(ja)])
      if (placeholders(key).join() !== placeholders(value).join())
        broken.push(`${key} → ${value}`);
    expect(broken).toEqual([]);
  });

  it("同一文案在不同文件中的译文一致", () => {
    const seen = new Map<string, string>();
    const conflicts: string[] = [];
    for (const [file, part] of Object.entries(localeParts))
      for (const [table, entries] of [
        ["en", part.en],
        ["ja", part.ja],
      ] as const)
        for (const [key, value] of Object.entries(entries)) {
          const id = `${table}:${key}`;
          const previous = seen.get(id);
          if (previous !== undefined && previous !== value)
            conflicts.push(`${file} ${id}`);
          seen.set(id, value);
        }
    expect(conflicts).toEqual([]);
  });

  it("标题字体子集包含界面用到的全部中日文字", () => {
    // Same character classes as web/scripts/subset-fonts.py.
    const cjkAll =
      /[\u3000-\u303f\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uff00-\uffef\u2018-\u201f\u2026\u00b7]/g;
    expect(charsetText, "web/src/fonts/charset.txt").toBeTypeOf("string");
    const charset = new Set(charsetText.trim());
    const texts = [...sources, ...localeTexts].map((file) => file.text);
    const missing = new Set<string>();
    for (const text of texts)
      for (const char of text.match(cjkAll) ?? [])
        if (!charset.has(char)) missing.add(char);
    // Rerun web/scripts/subset-fonts.py when this fails.
    expect([...missing].join("")).toBe("");
  });

  it("按语言查表并填入参数，缺少条目时显示中文", () => {
    setLang("en");
    expect(lang()).toBe("en");
    expect(t("MosDNS 服务中心")).toBe(en["MosDNS 服务中心"]);
    expect(t("不存在的文案 {n}", { n: 3 })).toBe("不存在的文案 3");
    expect(document.documentElement.lang).toBe("en");
    setLang("ja");
    expect(document.documentElement.lang).toBe("ja");
    setLang("zh");
    expect(t("MosDNS 服务中心")).toBe("MosDNS 服务中心");
    expect(msg("任意")).toBe("任意");
  });

  it("优先使用已保存的语言，其次是浏览器语言，默认英文", () => {
    expect(detectLang("ja", ["zh-CN"])).toBe("ja");
    expect(detectLang(null, ["zh-TW", "en"])).toBe("zh");
    expect(detectLang(null, ["ja-JP"])).toBe("ja");
    expect(detectLang("fr", ["fr-FR", "de"])).toBe("en");
  });
});
