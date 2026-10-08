import { t } from "./i18n";
import type { RuntimeProxy } from "./types";

// Ciphers the server accepts (pkg/upstream/dialer): Shadowsocks 2022 and
// AEAD. Stream ciphers and "none" are refused there too.
export const shadowsocksMethods = [
  "2022-blake3-aes-128-gcm",
  "2022-blake3-aes-256-gcm",
  "2022-blake3-chacha20-poly1305",
  "aes-128-gcm",
  "aes-192-gcm",
  "aes-256-gcm",
  "chacha20-ietf-poly1305",
  "xchacha20-ietf-poly1305",
];

// base64Bytes decodes share-link base64 (standard or URL-safe, padding
// optional), or with strict only padded standard base64, which is what the
// server's 2022 key decoder accepts.
function base64Bytes(text: string, strict = false): Uint8Array | null {
  const value = strict
    ? text
    : text.replace(/-/g, "+").replace(/_/g, "/").replace(/=+$/, "");
  const shape = strict
    ? /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/
    : /^[A-Za-z0-9+/]*$/;
  if (!value || !shape.test(value) || value.length % 4 === 1) return null;
  try {
    const padded = value + "=".repeat((4 - (value.length % 4)) % 4);
    return Uint8Array.from(atob(padded), (char) => char.charCodeAt(0));
  } catch {
    return null;
  }
}

function base64Text(text: string) {
  const bytes = base64Bytes(text);
  if (!bytes) return null;
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return null;
  }
}

export function validProxyServer(server: string) {
  const match = /^(?:\[([0-9a-fA-F:.]+)\]|([^\s:/?#@[\]]+)):(\d{1,5})$/.exec(
    server,
  );
  const port = Number(match?.[3]);
  return Boolean(match) && port >= 1 && port <= 65535;
}

function split(text: string, separator: string, last = false) {
  const at = last ? text.lastIndexOf(separator) : text.indexOf(separator);
  return at < 0 ? null : [text.slice(0, at), text.slice(at + 1)];
}

// parseProxyLink reads ss:// (SIP022, SIP002 and legacy share links) and
// socks5:// links in the browser; nothing is sent anywhere.
export function parseProxyLink(raw: string): RuntimeProxy | null {
  const text = raw.trim();
  const scheme = /^([a-z0-9]+):\/\//i.exec(text)?.[1]?.toLowerCase();
  const rest = scheme ? text.slice(scheme.length + 3).split("#")[0] : "";
  try {
    if (scheme === "ss") return parseShadowsocks(rest);
    if (scheme === "socks5" || scheme === "socks5h") return parseSocks5(rest);
  } catch {
    // malformed percent-encoding
  }
  return null;
}

function parseShadowsocks(link: string): RuntimeProxy | null {
  let rest = link;
  const query = rest.indexOf("?");
  if (query >= 0) {
    if (new URLSearchParams(rest.slice(query + 1)).has("plugin")) return null;
    rest = rest.slice(0, query);
  }
  rest = rest.replace(/\/$/, "");
  let credentials: string[] | null;
  let server: string;
  const parts = split(rest, "@", true);
  if (!parts) {
    // Legacy: BASE64(method:password@host:port)
    const decoded = base64Text(rest);
    const inner = decoded ? split(decoded, "@", true) : null;
    if (!inner) return null;
    credentials = split(inner[0], ":");
    server = inner[1];
  } else {
    const [userinfo, host] = parts;
    server = host;
    if (userinfo.includes(":")) {
      // SIP022: method:password, percent-encoded
      const [method, password] = split(userinfo, ":")!;
      credentials = [decodeURIComponent(method), decodeURIComponent(password)];
    } else {
      // SIP002: BASE64(method:password)
      const decoded = base64Text(decodeURIComponent(userinfo));
      credentials = decoded ? split(decoded, ":") : null;
    }
  }
  if (!credentials) return null;
  const method = credentials[0].toLowerCase();
  const password = credentials[1];
  if (!shadowsocksMethods.includes(method) || !password) return null;
  if (!validProxyServer(server)) return null;
  return { type: "shadowsocks", server, method, password };
}

function parseSocks5(link: string): RuntimeProxy | null {
  const rest = link.replace(/[/?].*$/, "");
  const parts = split(rest, "@", true);
  const server = parts ? parts[1] : rest;
  if (!validProxyServer(server)) return null;
  const proxy: RuntimeProxy = { type: "socks5", server };
  if (parts) {
    const [username, password = ""] = split(parts[0], ":") ?? [parts[0]];
    if (username) proxy.username = decodeURIComponent(username);
    if (password) proxy.password = decodeURIComponent(password);
  }
  return proxy;
}

// The 2022 ciphers take one or more ":"-joined base64 keys of a fixed size.
function shadowsocksKeyBytes(method: string) {
  if (!method.startsWith("2022-")) return 0;
  return method === "2022-blake3-aes-128-gcm" ? 16 : 32;
}

export function proxyDraftError(proxy: RuntimeProxy, addr = "") {
  // udpme dials by itself; the server refuses a proxy on it.
  if (/^udpme:\/\//i.test(addr.trim()))
    return t("udpme 上游不能使用代理，请改用 udp:// 地址。");
  if (!validProxyServer(proxy.server.trim()))
    return t("代理服务器需写成“主机:端口”，例如 hk.example.net:8388。");
  if (proxy.type !== "shadowsocks") return "";
  if (!shadowsocksMethods.includes(proxy.method ?? ""))
    return t("请选择 Shadowsocks 加密方式。");
  const password = proxy.password ?? "";
  if (!password)
    return proxy.password_set ? "" : t("请填写 Shadowsocks 密钥。");
  if (
    proxy.method === "2022-blake3-chacha20-poly1305" &&
    password.includes(":")
  )
    return t("{method} 只能使用一个密钥。", { method: proxy.method });
  const bytes = shadowsocksKeyBytes(proxy.method!);
  if (
    bytes &&
    password.split(":").some((key) => base64Bytes(key, true)?.length !== bytes)
  )
    return t("{method} 的密钥需为 {bytes} 字节数据的 Base64 编码。", {
      method: proxy.method!,
      bytes,
    });
  return "";
}

export const proxyTypeNames: Record<RuntimeProxy["type"], string> = {
  shadowsocks: "Shadowsocks",
  socks5: "SOCKS5",
};

// Changing where a proxy points means its saved secret no longer applies;
// the server refuses to reuse it, so ask for a new one up front.
export function updateProxy(
  proxy: RuntimeProxy,
  patch: Partial<RuntimeProxy>,
): RuntimeProxy {
  const next = { ...proxy, ...patch };
  if (
    next.server !== proxy.server ||
    next.method !== proxy.method ||
    next.username !== proxy.username ||
    next.type !== proxy.type
  )
    next.password_set = false;
  return next;
}
