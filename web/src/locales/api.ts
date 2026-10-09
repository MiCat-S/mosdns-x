// Translations for the Chinese message keys used in api.ts.
export const en: Record<string, string> = {
  "请求失败（{status}）": "Request failed ({status})",
  "你没有执行此操作的权限。": "You don't have permission to do this.",
  "当前未启用托管运行配置。你仍可查看只读摘要；如需验证、应用和回滚，请按当前版本说明配置托管文件。":
    "Managed runtime config is not enabled. You can still view the read-only summary; to validate, apply and roll back, set up the managed file as described for this version.",
  "配置已被其他操作更新。请基于最新运行基线核对草稿并重新验证。":
    "The config was changed by another operation. Check your draft against the latest running baseline and validate again.",
  "候选配置校验失败，请检查对应字段或查看技术详情。":
    "The candidate config failed validation. Check the fields concerned or see the technical details.",
  "验证结果已失效，请重新验证当前内容后继续。":
    "The validation result is no longer valid. Validate the current content again to continue.",
  "验证结果已过期，请重新验证当前内容后继续。":
    "The validation result has expired. Validate the current content again to continue.",
  "无法读取主配置来源，请检查托管文件是否存在及服务账号是否有读取权限。":
    "Can't read the main config source. Check that the managed file exists and that the service account can read it.",
  "当前节点无法提供运行配置摘要，请检查节点版本与运行状态。":
    "This node can't provide a runtime config summary. Check the node's version and status.",
  "请求失败，请稍后重试。": "The request failed. Please try again later.",
  "请输入有效的域名，例如 example.com。":
    "Enter a valid domain, e.g. example.com.",
  "部分缓存未能清除，请查看服务日志后重试。":
    "Some caches could not be cleared. Check the service log and try again.",
  "另一次清除正在进行，请稍后再试。":
    "Another purge is still running. Try again in a moment.",
};

export const ja: Record<string, string> = {
  "请求失败（{status}）": "リクエストに失敗しました（{status}）",
  "你没有执行此操作的权限。": "この操作を行う権限がありません。",
  "当前未启用托管运行配置。你仍可查看只读摘要；如需验证、应用和回滚，请按当前版本说明配置托管文件。":
    "マネージド実行設定は有効になっていません。読み取り専用の概要は引き続き確認できます。検証・適用・ロールバックを行うには、このバージョンの説明に従ってマネージドファイルを設定してください。",
  "配置已被其他操作更新。请基于最新运行基线核对草稿并重新验证。":
    "設定が別の操作で更新されました。最新の実行ベースラインと下書きを照合し、もう一度検証してください。",
  "候选配置校验失败，请检查对应字段或查看技术详情。":
    "候補設定の検証に失敗しました。該当する項目を確認するか、技術的な詳細をご覧ください。",
  "验证结果已失效，请重新验证当前内容后继续。":
    "検証結果が無効になりました。現在の内容をもう一度検証してから続行してください。",
  "验证结果已过期，请重新验证当前内容后继续。":
    "検証結果の有効期限が切れました。現在の内容をもう一度検証してから続行してください。",
  "无法读取主配置来源，请检查托管文件是否存在及服务账号是否有读取权限。":
    "メイン設定のソースを読み込めません。マネージドファイルが存在し、サービスアカウントに読み取り権限があるか確認してください。",
  "当前节点无法提供运行配置摘要，请检查节点版本与运行状态。":
    "このノードは実行設定の概要を提供できません。ノードのバージョンと稼働状態を確認してください。",
  "请求失败，请稍后重试。":
    "リクエストに失敗しました。しばらくしてからもう一度お試しください。",
  "请输入有效的域名，例如 example.com。":
    "有効なドメインを入力してください（例：example.com）。",
  "部分缓存未能清除，请查看服务日志后重试。":
    "一部のキャッシュを消去できませんでした。サービスログを確認して再試行してください。",
  "另一次清除正在进行，请稍后再试。":
    "別の消去処理が実行中です。しばらくしてからもう一度お試しください。",
};
