import { describe, expect, it } from "vitest";
import { parseProxyLink, proxyDraftError, updateProxy } from "./proxy";

const key16 = "AAECAwQFBgcICQoLDA0ODw=="; // 16 bytes
const b64 = (text: string) => btoa(text);

describe("代理链接与校验", () => {
  it("解析 SIP022、SIP002、整段分享链接与 socks5 链接", () => {
    expect(
      parseProxyLink(
        `ss://2022-blake3-aes-128-gcm:${encodeURIComponent(key16)}@hk.example.net:8388#香港`,
      ),
    ).toEqual({
      type: "shadowsocks",
      server: "hk.example.net:8388",
      method: "2022-blake3-aes-128-gcm",
      password: key16,
    });
    const sip002 = b64("aes-256-gcm:pa:ss").replace(/=+$/, "");
    expect(parseProxyLink(`ss://${sip002}@[2001:db8::1]:443/?#x`)).toEqual({
      type: "shadowsocks",
      server: "[2001:db8::1]:443",
      method: "aes-256-gcm",
      password: "pa:ss",
    });
    expect(
      parseProxyLink(`ss://${b64("chacha20-ietf-poly1305:p@ss@1.2.3.4:8389")}`),
    ).toEqual({
      type: "shadowsocks",
      server: "1.2.3.4:8389",
      method: "chacha20-ietf-poly1305",
      password: "p@ss",
    });
    expect(parseProxyLink("socks5://u%40x:p%3Aw@127.0.0.1:1080")).toEqual({
      type: "socks5",
      server: "127.0.0.1:1080",
      username: "u@x",
      password: "p:w",
    });
    expect(parseProxyLink("socks5://proxy.example:1080")).toEqual({
      type: "socks5",
      server: "proxy.example:1080",
    });
  });

  it("拒绝不支持或不完整的链接", () => {
    for (const link of [
      "ss://rc4-md5:secret@1.2.3.4:8388",
      "ss://none:secret@1.2.3.4:8388",
      "ss://aes-128-gcm:secret@1.2.3.4",
      "ss://aes-128-gcm:secret@1.2.3.4:8388/?plugin=obfs-local",
      "ss://aes-128-gcm:%E0%A4%A@1.2.3.4:8388",
      "http://1.2.3.4:8080",
      "socks5://1.2.3.4",
      "not a link",
    ])
      expect(parseProxyLink(link)).toBeNull();
  });

  it("检查服务器、加密方式与 2022 密钥长度", () => {
    const ss = {
      type: "shadowsocks" as const,
      server: "h:1",
      method: "2022-blake3-aes-128-gcm",
    };
    expect(proxyDraftError({ ...ss, password: key16 })).toBe("");
    expect(proxyDraftError({ ...ss, password: `${key16}:${key16}` })).toBe("");
    expect(proxyDraftError({ ...ss, password_set: true })).toBe("");
    expect(proxyDraftError(ss)).toBe("请填写 Shadowsocks 密钥。");
    expect(proxyDraftError({ ...ss, password: "c2hvcnQ=" })).toMatch(/16 字节/);
    expect(
      proxyDraftError({
        ...ss,
        method: "2022-blake3-aes-256-gcm",
        password: key16,
      }),
    ).toMatch(/32 字节/);
    // The server's key decoder wants padded standard base64.
    expect(
      proxyDraftError({ ...ss, password: key16.replace(/=+$/, "") }),
    ).toMatch(/16 字节/);
    expect(
      proxyDraftError({ ...ss, method: "aes-256-gcm", password: "any" }),
    ).toBe("");
    expect(proxyDraftError({ ...ss, server: "h", password: key16 })).toMatch(
      /主机:端口/,
    );
    expect(proxyDraftError({ type: "socks5", server: "h:99999" })).toMatch(
      /主机:端口/,
    );
    expect(proxyDraftError({ type: "socks5", server: "h:1080" })).toBe("");
    expect(
      proxyDraftError({ type: "socks5", server: "h:1080" }, "udpme://1.1.1.1"),
    ).toMatch(/udpme/);
    expect(
      proxyDraftError({
        ...ss,
        method: "2022-blake3-chacha20-poly1305",
        password: `${btoa("x".repeat(32))}:${btoa("y".repeat(32))}`,
      }),
    ).toMatch(/只能使用一个密钥/);
  });

  it("修改服务器或加密方式后不再沿用已保存的密钥", () => {
    const saved = {
      type: "shadowsocks" as const,
      server: "h:1",
      method: "aes-128-gcm",
      password_set: true,
    };
    expect(updateProxy(saved, { password: "new" }).password_set).toBe(true);
    expect(updateProxy(saved, { server: "other:1" }).password_set).toBe(false);
    expect(updateProxy(saved, { method: "aes-256-gcm" }).password_set).toBe(
      false,
    );
  });
});
