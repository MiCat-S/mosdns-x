// Translations for the Chinese message keys used in the matching source
// file. Keys must match the t()/msg() literals exactly.
export const en: Record<string, string> = {
  "此插件含敏感连接参数，不能在面板中读取或修改。":
    "This plugin has sensitive connection parameters and can't be viewed or edited in the panel.",
  "此插件包含当前面板不支持的参数，保持只读。":
    "This plugin has parameters the panel doesn't support yet, so it stays read-only.",
  "此插件类型暂不支持在运行配置中编辑。":
    "This plugin type can't be edited in the runtime config yet.",
  "请先修正标记为无效的数值字段。":
    "Fix the numeric fields marked as invalid first.",
  "上游地址不能为空，请检查标记的字段。":
    "Upstream addresses can't be empty. Check the marked fields.",
  "请输入 {min} 到 {max} 的整数。": "Enter a whole number from {min} to {max}.",
  "上游 {index}": "Upstream {index}",
  上游地址: "Upstream address",
  "上游 {index} 地址": "Upstream {index} address",
  "例如：https://dns.example/dns-query…": "e.g. https://dns.example/dns-query…",
  日志显示名称: "Display name in logs",
  "可选。留空时公共 DNS 显示地址，其他上游只显示插件名和序号，不显示地址。":
    "Optional. If left empty, public DNS servers show their address; other upstreams show only the plugin name and number, never the address.",
  "上游 {index} 日志显示名称": "Upstream {index} display name in logs",
  "例如：香港私有 DoH…": "e.g. Hong Kong private DoH…",
  删除: "Delete",
  高级连接选项: "Advanced connection options",
  拨号地址: "Dial address",
  "可选，用于覆盖连接目标。": "Optional. Overrides the connection target.",
  绑定网卡: "Bind to interface",
  "空闲超时（秒）": "Idle timeout (seconds)",
  最大连接数: "Max connections",
  信任上游应答: "Trust upstream responses",
  启用连接复用: "Enable connection reuse",
  "跳过 TLS 证书校验": "Skip TLS certificate verification",
  "启用内核 TX": "Enable kernel TX",
  "启用内核 RX": "Enable kernel RX",
  "上游 · {tag}": "Upstream · {tag}",
  "仅显示并编辑已通过安全检查的连接字段。探测结果不包含上游地址。":
    "Only connection fields that passed the safety check are shown and editable. Probe results don't include upstream addresses.",
  添加上游: "Add upstream",
  "探测中…": "Probing…",
  探测上游: "Probe upstreams",
  上游探测结果: "Upstream probe results",
  成功: "Succeeded",
  失败: "Failed",
  "未收到 DNS 应答": "No DNS response received",
  "内存缓存 · {tag}": "Memory cache · {tag}",
  缓存容量: "Cache size",
  "懒缓存 TTL（秒）": "Lazy cache TTL (seconds)",
  "懒缓存应答 TTL（秒）": "Lazy cache reply TTL (seconds)",
  "压缩 DNS 应答": "Compress DNS responses",
  "此插件当前只能查看标签和类型。":
    "Only this plugin's tag and type can be viewed for now.",
  可读取: "Readable",
  最近一次加载时已读取: "Read at last load",
  暂不可用: "Unavailable",
  未配置: "Not configured",
  "未配置托管文件，仅支持查看。": "No managed file is configured; view only.",
  "由主配置管理，修改后需要重启。":
    "Managed by the main config; changes require a restart.",
  "包含敏感连接参数，仅展示安全摘要。":
    "Has sensitive connection parameters; only a safe summary is shown.",
  "包含当前面板不支持的参数。": "Has parameters the panel doesn't support yet.",
  "当前插件类型只支持查看。": "This plugin type is view-only.",
  "未提供额外说明。": "No further details.",
  已启用查询明细记录: "Query detail logging enabled",
  已停用查询明细记录: "Query detail logging disabled",
  "聚合数据保留期：{from} → {to}": "Aggregate data retention: {from} → {to}",
  "查询明细保留期：{from} → {to}": "Query detail retention: {from} → {to}",
  "查询明细条数上限：{from} → {to}": "Query detail record limit: {from} → {to}",
  "上游插件 {tag} 的连接参数已修改":
    "Connection parameters of upstream plugin {tag} changed",
  "缓存插件 {tag} 的参数已修改": "Parameters of cache plugin {tag} changed",
  修改预览: "Change preview",
  "尚未修改可托管的运行参数。":
    "No managed runtime parameters have been changed yet.",
  修订历史: "Revision history",
  "最多保存 10 个旧修订。具备托管能力且当前摘要可用时，可以回滚到其他版本。":
    "Up to 10 old revisions are kept. When managed config is available and the current summary is readable, you can roll back to another revision.",
  重试历史: "Retry history",
  当前: "Current",
  回滚: "Roll back",
  尚无可回滚的历史修订: "No revisions to roll back to yet",
  "运行配置还有未保存的修改，确定离开当前页面吗？":
    "The runtime config has unsaved changes. Leave this page anyway?",
  "运行配置还有未保存的修改，确定退出登录吗？":
    "The runtime config has unsaved changes. Sign out anyway?",
  "运行配置还有未保存的修改，确定放弃当前修改吗？":
    "The runtime config has unsaved changes. Discard them?",
  "验证通过。应用后会清空 DNS 缓存。":
    "Validation passed. Applying will clear the DNS cache.",
  "验证通过。可以应用本次修改。":
    "Validation passed. You can apply these changes.",
  "已载入最新运行基线，并保留你的草稿；请核对差异后重新验证。":
    "Loaded the latest running baseline and kept your draft. Review the differences and validate again.",
  "最新运行基线加载失败：{error}；本地草稿仍保留。":
    "Couldn't load the latest running baseline: {error}. Your local draft is kept.",
  "运行配置已应用，DNS 缓存已清空。":
    "Runtime config applied. DNS cache cleared.",
  "运行配置已应用。": "Runtime config applied.",
  "主配置包含需要重启的项：{items}。当前运行配置未切换。":
    "The main config has items that require a restart: {items}. The running config was not switched.",
  "、": ", ",
  "主配置已重读，DNS 缓存已清空。": "Main config reloaded. DNS cache cleared.",
  "主配置已重读。": "Main config reloaded.",
  "已回滚运行配置，DNS 缓存已清空。":
    "Runtime config rolled back. DNS cache cleared.",
  "已回滚运行配置。": "Runtime config rolled back.",
  运行配置: "Runtime config",
  "分别查看正在运行的安全摘要，并在启用托管后验证、应用和回滚支持的设置。":
    "View a safe summary of what is running and, once managed config is enabled, validate, apply and roll back supported settings.",
  重新读取摘要: "Reload summary",
  "重读中…": "Reloading…",
  重读主配置: "Reload main config",
  运行配置摘要加载失败: "Couldn't load the runtime config summary",
  "加载失败与未配置托管文件是不同状态。请重试以确认当前节点能力。":
    "A failed load is not the same as having no managed file. Retry to confirm what this node supports.",
  重试摘要: "Retry summary",
  "当前为只读模式。": "Read-only mode.",
  "未启用托管配置不会影响 DNS 服务；当前页面仍展示服务端已脱敏的运行摘要。验证、应用、重读、历史和回滚需按当前版本说明配置托管文件。":
    "Leaving managed config off doesn't affect the DNS service; this page still shows the server's redacted runtime summary. To validate, apply, reload, view history or roll back, set up a managed file as described for this version.",
  运行状态: "Runtime status",
  当前修订: "Current revision",
  尚未保存: "Not saved yet",
  管理模式: "Management mode",
  托管管理: "Managed",
  只读检查: "Read-only inspection",
  可编辑插件: "Editable plugins",
  "{count} 个": "{count}",
  正在运行: "Running",
  "页面编辑基于此运行摘要。":
    "Edits on this page start from this running summary.",
  已加载的主配置基线: "Loaded main config baseline",
  "这是最近一次成功加载的副本；磁盘文件之后可能已变化。":
    "This is the copy from the last successful load; the file on disk may have changed since.",
  候选配置: "Candidate config",
  "未应用候选不会标记为正在运行。":
    "A candidate that hasn't been applied is never marked as running.",
  查询与统计: "Queries and statistics",
  记录查询明细: "Log query details",
  聚合保留天数: "Aggregate retention (days)",
  "1 至 31 天": "1 to 31 days",
  查询明细保留小时: "Query detail retention (hours)",
  "1 至 720 小时": "1 to 720 hours",
  查询明细条数上限: "Query detail record limit",
  "1000 至 5000000 条": "1000 to 5000000 records",
  重试探测: "Retry probe",
  应用配置: "Apply config",
  "验证会完整构建候选运行代，但不会切换 DNS 服务。验证令牌仅在当前管理员会话中有效 5 分钟，且只能应用一次。":
    "Validation fully builds a candidate runtime generation but doesn't switch the DNS service. The validation token is valid for 5 minutes in the current administrator session and can be applied only once.",
  放弃修改: "Discard changes",
  "验证中…": "Validating…",
  验证修改: "Validate changes",
  "应用中…": "Applying…",
  应用已验证的修改: "Apply validated changes",
  "验证令牌有效至 {time}。": "Validation token valid until {time}.",
  节点数据源: "Node data sources",
  "这些数据源来自主配置并参与既有分流。运行时暂不提供的条目数和加载时间不会推测为 0。":
    "These data sources come from the main config and take part in existing routing. Entry counts and load times the runtime doesn't report yet are not assumed to be 0.",
  来源暂不可用: "Source unavailable",
  自动重载: "Auto reload",
  不自动重载: "No auto reload",
  "条目：{entries} · 最近加载：{loaded}":
    "Entries: {entries} · Last loaded: {loaded}",
  插件能力: "Plugin capabilities",
  可编辑: "Editable",
  只读: "Read-only",
  可热更新: "Hot reloadable",
  修改需重启: "Restart required for changes",
  不可热更新: "Not hot reloadable",
  应用后清空内存缓存: "Applying clears memory caches",
  应用不清空内存缓存: "Applying doesn't clear memory caches",
  查看安全摘要: "View safe summary",
  只读插件: "Read-only plugins",
  当前配置没有只读插件: "The current config has no read-only plugins",
  "请先修正标记的代理设置。": "Fix the marked proxy settings first.",
  "无法识别该链接。支持 ss:// 与 socks5:// 链接，加密方式需为 2022 或 AEAD。":
    "This link wasn't recognized. ss:// and socks5:// links are supported, with a 2022 or AEAD cipher.",
  "已保存；留空则保持不变，填写则替换。":
    "Saved. Leave empty to keep it, or enter a new one to replace it.",
  代理: "Proxy",
  未填写服务器: "no server yet",
  不使用: "Not used",
  从链接导入: "Import from link",
  "支持 ss:// 与 socks5:// 链接，在浏览器中解析后填入下方字段。":
    "Accepts ss:// and socks5:// links. The link is parsed in your browser and fills in the fields below.",
  "上游 {index} 代理链接": "Upstream {index} proxy link",
  导入: "Import",
  代理类型: "Proxy type",
  "上游 {index} 代理类型": "Upstream {index} proxy type",
  不使用代理: "No proxy",
  代理服务器: "Proxy server",
  "主机:端口": "host:port",
  "上游 {index} 代理服务器": "Upstream {index} proxy server",
  加密方式: "Cipher",
  "上游 {index} 加密方式": "Upstream {index} cipher",
  密钥: "Key",
  "2022 加密方式填写 Base64 密钥，其他方式填写密码。":
    "2022 ciphers take a Base64 key; other ciphers take a password.",
  "上游 {index} 代理密钥": "Upstream {index} proxy key",
  已保存: "Saved",
  可选: "Optional",
  "上游 {index} 代理用户名": "Upstream {index} proxy username",
  "上游 {index} 代理密码": "Upstream {index} proxy password",
  "TCP 与 UDP 查询都经代理转发；UDP、DoQ、DoH3 上游需要代理服务器支持 UDP。密钥只保存在服务器上，保存后面板不再显示。":
    "Both TCP and UDP queries go through the proxy; UDP, DoQ and DoH3 upstreams need a proxy server that relays UDP. The key is stored only on the server and is not shown again after saving.",
};

export const ja: Record<string, string> = {
  "此插件含敏感连接参数，不能在面板中读取或修改。":
    "このプラグインには機密性の高い接続パラメーターが含まれるため、パネルでは表示も変更もできません。",
  "此插件包含当前面板不支持的参数，保持只读。":
    "このプラグインには現在のパネルが対応していないパラメーターが含まれるため、読み取り専用です。",
  "此插件类型暂不支持在运行配置中编辑。":
    "このプラグインの種類は、実行設定での編集にまだ対応していません。",
  "请先修正标记为无效的数值字段。":
    "無効と表示された数値フィールドを先に修正してください。",
  "上游地址不能为空，请检查标记的字段。":
    "アップストリームのアドレスは空にできません。表示されたフィールドを確認してください。",
  "请输入 {min} 到 {max} 的整数。": "{min}～{max} の整数を入力してください。",
  "上游 {index}": "アップストリーム {index}",
  上游地址: "アップストリームのアドレス",
  "上游 {index} 地址": "アップストリーム {index} のアドレス",
  "例如：https://dns.example/dns-query…": "例：https://dns.example/dns-query…",
  日志显示名称: "ログ表示名",
  "可选。留空时公共 DNS 显示地址，其他上游只显示插件名和序号，不显示地址。":
    "任意。空欄の場合、公開 DNS はアドレスを表示し、その他のアップストリームはアドレスを表示せず、プラグイン名と番号のみを表示します。",
  "上游 {index} 日志显示名称": "アップストリーム {index} のログ表示名",
  "例如：香港私有 DoH…": "例：香港のプライベート DoH…",
  删除: "削除",
  高级连接选项: "詳細な接続オプション",
  拨号地址: "ダイヤルアドレス",
  "可选，用于覆盖连接目标。": "任意。接続先を上書きします。",
  绑定网卡: "バインドするネットワークインターフェース",
  "空闲超时（秒）": "アイドルタイムアウト（秒）",
  最大连接数: "最大接続数",
  信任上游应答: "アップストリームの応答を信頼",
  启用连接复用: "接続の再利用を有効化",
  "跳过 TLS 证书校验": "TLS 証明書の検証をスキップ",
  "启用内核 TX": "カーネル TX を有効化",
  "启用内核 RX": "カーネル RX を有効化",
  "上游 · {tag}": "アップストリーム · {tag}",
  "仅显示并编辑已通过安全检查的连接字段。探测结果不包含上游地址。":
    "安全チェックを通過した接続フィールドのみ表示・編集できます。プローブ結果にアップストリームのアドレスは含まれません。",
  添加上游: "アップストリームを追加",
  "探测中…": "プローブ中…",
  探测上游: "アップストリームをプローブ",
  上游探测结果: "アップストリームのプローブ結果",
  成功: "成功",
  失败: "失敗",
  "未收到 DNS 应答": "DNS 応答なし",
  "内存缓存 · {tag}": "メモリキャッシュ · {tag}",
  缓存容量: "キャッシュ容量",
  "懒缓存 TTL（秒）": "遅延キャッシュ TTL（秒）",
  "懒缓存应答 TTL（秒）": "遅延キャッシュ応答 TTL（秒）",
  "压缩 DNS 应答": "DNS 応答を圧縮",
  "此插件当前只能查看标签和类型。":
    "このプラグインは現在、タグと種類のみ表示できます。",
  可读取: "読み取り可能",
  最近一次加载时已读取: "前回の読み込み時に取得済み",
  暂不可用: "利用不可",
  未配置: "未設定",
  "未配置托管文件，仅支持查看。":
    "マネージドファイルが未設定のため、表示のみ可能です。",
  "由主配置管理，修改后需要重启。":
    "メイン設定で管理されているため、変更には再起動が必要です。",
  "包含敏感连接参数，仅展示安全摘要。":
    "機密性の高い接続パラメーターを含むため、安全な概要のみ表示します。",
  "包含当前面板不支持的参数。":
    "現在のパネルが対応していないパラメーターを含みます。",
  "当前插件类型只支持查看。":
    "このプラグインの種類は表示のみに対応しています。",
  "未提供额外说明。": "補足情報はありません。",
  已启用查询明细记录: "クエリ詳細の記録を有効化",
  已停用查询明细记录: "クエリ詳細の記録を無効化",
  "聚合数据保留期：{from} → {to}": "集計データの保持期間：{from} → {to}",
  "查询明细保留期：{from} → {to}": "クエリ詳細の保持期間：{from} → {to}",
  "查询明细条数上限：{from} → {to}": "クエリ詳細の件数上限：{from} → {to}",
  "上游插件 {tag} 的连接参数已修改":
    "アップストリームプラグイン {tag} の接続パラメーターを変更",
  "缓存插件 {tag} 的参数已修改":
    "キャッシュプラグイン {tag} のパラメーターを変更",
  修改预览: "変更のプレビュー",
  "尚未修改可托管的运行参数。":
    "マネージド対象の実行パラメーターはまだ変更されていません。",
  修订历史: "リビジョン履歴",
  "最多保存 10 个旧修订。具备托管能力且当前摘要可用时，可以回滚到其他版本。":
    "過去のリビジョンは最大 10 件まで保存されます。マネージド機能が利用でき、現在の概要を取得できる場合は、別のリビジョンにロールバックできます。",
  重试历史: "履歴を再試行",
  当前: "現在",
  回滚: "ロールバック",
  尚无可回滚的历史修订: "ロールバックできるリビジョンはまだありません",
  "运行配置还有未保存的修改，确定离开当前页面吗？":
    "実行設定に未保存の変更があります。このページを離れてもよろしいですか？",
  "运行配置还有未保存的修改，确定退出登录吗？":
    "実行設定に未保存の変更があります。ログアウトしてもよろしいですか？",
  "运行配置还有未保存的修改，确定放弃当前修改吗？":
    "実行設定に未保存の変更があります。現在の変更を破棄してもよろしいですか？",
  "验证通过。应用后会清空 DNS 缓存。":
    "検証に成功しました。適用すると DNS キャッシュがクリアされます。",
  "验证通过。可以应用本次修改。":
    "検証に成功しました。この変更を適用できます。",
  "已载入最新运行基线，并保留你的草稿；请核对差异后重新验证。":
    "最新の実行ベースラインを読み込み、下書きは保持しました。差分を確認してから再度検証してください。",
  "最新运行基线加载失败：{error}；本地草稿仍保留。":
    "最新の実行ベースラインを読み込めませんでした：{error}。ローカルの下書きは保持されています。",
  "运行配置已应用，DNS 缓存已清空。":
    "実行設定を適用し、DNS キャッシュをクリアしました。",
  "运行配置已应用。": "実行設定を適用しました。",
  "主配置包含需要重启的项：{items}。当前运行配置未切换。":
    "メイン設定に再起動が必要な項目があります：{items}。現在の実行設定は切り替えられていません。",
  "、": "、",
  "主配置已重读，DNS 缓存已清空。":
    "メイン設定を再読み込みし、DNS キャッシュをクリアしました。",
  "主配置已重读。": "メイン設定を再読み込みしました。",
  "已回滚运行配置，DNS 缓存已清空。":
    "実行設定をロールバックし、DNS キャッシュをクリアしました。",
  "已回滚运行配置。": "実行設定をロールバックしました。",
  运行配置: "実行設定",
  "分别查看正在运行的安全摘要，并在启用托管后验证、应用和回滚支持的设置。":
    "実行中の設定の安全な概要を確認できます。マネージド設定を有効にすると、対応する設定を検証・適用・ロールバックできます。",
  重新读取摘要: "概要を再取得",
  "重读中…": "再読み込み中…",
  重读主配置: "メイン設定を再読み込み",
  运行配置摘要加载失败: "実行設定の概要を読み込めませんでした",
  "加载失败与未配置托管文件是不同状态。请重试以确认当前节点能力。":
    "読み込みの失敗は、マネージドファイルが未設定の状態とは異なります。再試行して、このノードの機能を確認してください。",
  重试摘要: "概要を再試行",
  "当前为只读模式。": "読み取り専用モードです。",
  "未启用托管配置不会影响 DNS 服务；当前页面仍展示服务端已脱敏的运行摘要。验证、应用、重读、历史和回滚需按当前版本说明配置托管文件。":
    "マネージド設定を有効にしなくても DNS サービスには影響しません。このページには引き続き、サーバー側でマスキングされた実行概要が表示されます。検証・適用・再読み込み・履歴・ロールバックを使うには、現在のバージョンの説明に従ってマネージドファイルを設定してください。",
  运行状态: "実行状態",
  当前修订: "現在のリビジョン",
  尚未保存: "未保存",
  管理模式: "管理モード",
  托管管理: "マネージド",
  只读检查: "読み取り専用の確認",
  可编辑插件: "編集可能なプラグイン",
  "{count} 个": "{count} 個",
  正在运行: "実行中",
  "页面编辑基于此运行摘要。":
    "このページでの編集は、この実行概要をもとに行われます。",
  已加载的主配置基线: "読み込み済みのメイン設定ベースライン",
  "这是最近一次成功加载的副本；磁盘文件之后可能已变化。":
    "前回正常に読み込んだ時点のコピーです。ディスク上のファイルはその後変更されている可能性があります。",
  候选配置: "候補設定",
  "未应用候选不会标记为正在运行。":
    "未適用の候補が実行中と表示されることはありません。",
  查询与统计: "クエリと統計",
  记录查询明细: "クエリ詳細を記録",
  聚合保留天数: "集計の保持日数",
  "1 至 31 天": "1～31 日",
  查询明细保留小时: "クエリ詳細の保持時間",
  "1 至 720 小时": "1～720 時間",
  查询明细条数上限: "クエリ詳細の件数上限",
  "1000 至 5000000 条": "1000～5000000 件",
  重试探测: "プローブを再試行",
  应用配置: "設定を適用",
  "验证会完整构建候选运行代，但不会切换 DNS 服务。验证令牌仅在当前管理员会话中有效 5 分钟，且只能应用一次。":
    "検証では候補の実行世代を完全に構築しますが、DNS サービスは切り替えません。検証トークンは現在の管理者セッションでのみ 5 分間有効で、適用できるのは 1 回だけです。",
  放弃修改: "変更を破棄",
  "验证中…": "検証中…",
  验证修改: "変更を検証",
  "应用中…": "適用中…",
  应用已验证的修改: "検証済みの変更を適用",
  "验证令牌有效至 {time}。": "検証トークンは {time} まで有効です。",
  节点数据源: "ノードのデータソース",
  "这些数据源来自主配置并参与既有分流。运行时暂不提供的条目数和加载时间不会推测为 0。":
    "これらのデータソースはメイン設定に由来し、既存の振り分けに使われます。ランタイムがまだ提供していない件数や読み込み時刻を 0 とみなすことはありません。",
  来源暂不可用: "ソース利用不可",
  自动重载: "自動再読み込み",
  不自动重载: "自動再読み込みなし",
  "条目：{entries} · 最近加载：{loaded}":
    "件数：{entries} · 最終読み込み：{loaded}",
  插件能力: "プラグインの機能",
  可编辑: "編集可能",
  只读: "読み取り専用",
  可热更新: "ホットリロード可能",
  修改需重启: "変更には再起動が必要",
  不可热更新: "ホットリロード不可",
  应用后清空内存缓存: "適用するとメモリキャッシュをクリア",
  应用不清空内存缓存: "適用してもメモリキャッシュはクリアしない",
  查看安全摘要: "安全な概要を表示",
  只读插件: "読み取り専用プラグイン",
  当前配置没有只读插件: "現在の設定に読み取り専用プラグインはありません",
  "请先修正标记的代理设置。":
    "先に、マークされたプロキシ設定を修正してください。",
  "无法识别该链接。支持 ss:// 与 socks5:// 链接，加密方式需为 2022 或 AEAD。":
    "このリンクを認識できません。ss:// と socks5:// のリンクに対応しており、暗号方式は 2022 または AEAD である必要があります。",
  "已保存；留空则保持不变，填写则替换。":
    "保存済みです。空欄のままなら変更せず、入力すると置き換えます。",
  代理: "プロキシ",
  未填写服务器: "サーバー未入力",
  不使用: "使用しない",
  从链接导入: "リンクから読み込む",
  "支持 ss:// 与 socks5:// 链接，在浏览器中解析后填入下方字段。":
    "ss:// と socks5:// のリンクに対応しています。リンクはブラウザー内で解析され、下の項目に入力されます。",
  "上游 {index} 代理链接": "アップストリーム {index} のプロキシリンク",
  导入: "読み込む",
  代理类型: "プロキシの種類",
  "上游 {index} 代理类型": "アップストリーム {index} のプロキシの種類",
  不使用代理: "プロキシを使用しない",
  代理服务器: "プロキシサーバー",
  "主机:端口": "ホスト:ポート",
  "上游 {index} 代理服务器": "アップストリーム {index} のプロキシサーバー",
  加密方式: "暗号方式",
  "上游 {index} 加密方式": "アップストリーム {index} の暗号方式",
  密钥: "キー",
  "2022 加密方式填写 Base64 密钥，其他方式填写密码。":
    "2022 暗号方式では Base64 のキーを、その他の方式ではパスワードを入力します。",
  "上游 {index} 代理密钥": "アップストリーム {index} のプロキシキー",
  已保存: "保存済み",
  可选: "任意",
  "上游 {index} 代理用户名": "アップストリーム {index} のプロキシユーザー名",
  "上游 {index} 代理密码": "アップストリーム {index} のプロキシパスワード",
  "TCP 与 UDP 查询都经代理转发；UDP、DoQ、DoH3 上游需要代理服务器支持 UDP。密钥只保存在服务器上，保存后面板不再显示。":
    "TCP と UDP のクエリはどちらもプロキシ経由で転送されます。UDP・DoQ・DoH3 のアップストリームには UDP を中継できるプロキシサーバーが必要です。キーはサーバーにのみ保存され、保存後はパネルに表示されません。",
};
