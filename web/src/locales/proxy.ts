// Translations for the Chinese message keys used in proxy.ts.
export const en: Record<string, string> = {
  "代理服务器需写成“主机:端口”，例如 hk.example.net:8388。":
    'Write the proxy server as "host:port", e.g. hk.example.net:8388.',
  "请选择 Shadowsocks 加密方式。": "Choose a Shadowsocks cipher.",
  "请填写 Shadowsocks 密钥。": "Enter the Shadowsocks key.",
  "{method} 的密钥需为 {bytes} 字节数据的 Base64 编码。":
    "The key for {method} must be {bytes} bytes encoded in Base64.",
  "udpme 上游不能使用代理，请改用 udp:// 地址。":
    "udpme upstreams can't use a proxy; use a udp:// address instead.",
  "{method} 只能使用一个密钥。": "{method} takes only one key.",
};

export const ja: Record<string, string> = {
  "代理服务器需写成“主机:端口”，例如 hk.example.net:8388。":
    "プロキシサーバーは「ホスト:ポート」の形式で入力してください（例：hk.example.net:8388）。",
  "请选择 Shadowsocks 加密方式。": "Shadowsocks の暗号方式を選択してください。",
  "请填写 Shadowsocks 密钥。": "Shadowsocks のキーを入力してください。",
  "{method} 的密钥需为 {bytes} 字节数据的 Base64 编码。":
    "{method} のキーは {bytes} バイトのデータを Base64 でエンコードしたものである必要があります。",
  "udpme 上游不能使用代理，请改用 udp:// 地址。":
    "udpme のアップストリームではプロキシを使用できません。udp:// のアドレスを使用してください。",
  "{method} 只能使用一个密钥。": "{method} で使用できるキーは 1 つだけです。",
};
