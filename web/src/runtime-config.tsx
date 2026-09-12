import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { unstable_usePrompt } from "react-router-dom";
import { APIError, json, message, request } from "./api";
import {
  Alert,
  Card,
  Empty,
  Field,
  PageTitle,
  Spinner,
  fmt,
  useLoad,
  useUnsavedGuard,
} from "./components";
import type {
  RuntimeApplyResult,
  RuntimeCache,
  RuntimeConfig,
  RuntimeFastForward,
  RuntimePlugin,
  RuntimeProbe,
  RuntimeReloadResult,
  RuntimeRevision,
  RuntimeState,
  RuntimeTelemetry,
  RuntimeUpstream,
  RuntimeValidation,
} from "./types";

const readonlyReason: Record<string, string> = {
  sensitive_parameters: "此插件含敏感连接参数，不能在面板中读取或修改。",
  unsupported_parameters: "此插件包含当前面板不支持的参数，保持只读。",
  unsupported_type: "此插件类型暂不支持在运行配置中编辑。",
};

function copyConfig(config: RuntimeConfig): RuntimeConfig {
  return JSON.parse(JSON.stringify(config)) as RuntimeConfig;
}

function runtimeDraftError(config: RuntimeConfig) {
  const numeric: Array<[number, number, number]> = [
    [config.telemetry.aggregate_retention_days, 1, 31],
    [config.telemetry.query_retention_hours, 1, 720],
    [config.telemetry.max_query_records, 1000, 5_000_000],
  ];
  if (
    numeric.some(
      ([value, min, max]) =>
        !Number.isInteger(value) || value < min || value > max,
    )
  )
    return "请先修正标记为无效的数值字段。";
  if (
    config.plugins.some(
      (plugin) =>
        plugin.editable &&
        plugin.fast_forward?.upstreams.some(
          (upstream) => !upstream.addr.trim(),
        ),
    )
  )
    return "上游地址不能为空，请检查标记的字段。";
  return "";
}

function inputNumber(value: string) {
  return Number(value);
}

function updateValue<T extends object, K extends keyof T>(
  value: T,
  key: K,
  next: T[K],
): T {
  return { ...value, [key]: next };
}

function RuntimeNumberField({
  label,
  value,
  min,
  max,
  hint,
  disabled,
  onChange,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  hint?: string;
  disabled?: boolean;
  onChange: (value: number) => void;
}) {
  const invalid = !Number.isInteger(value) || value < min || value > max;
  return (
    <Field
      label={label}
      hint={invalid ? `请输入 ${min} 到 ${max} 的整数。` : hint}
    >
      <input
        type="number"
        min={min}
        max={max}
        value={value}
        disabled={disabled}
        aria-invalid={invalid || undefined}
        onChange={(event) => onChange(inputNumber(event.target.value))}
      />
    </Field>
  );
}

function RuntimeSwitch({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className="runtime-switch">
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span>{label}</span>
    </label>
  );
}

function RuntimeUpstreamEditor({
  upstream,
  index,
  disabled,
  onChange,
  onRemove,
  removable,
}: {
  upstream: RuntimeUpstream;
  index: number;
  disabled: boolean;
  onChange: (patch: Partial<RuntimeUpstream>) => void;
  onRemove: () => void;
  removable: boolean;
}) {
  const update = <K extends keyof RuntimeUpstream>(
    key: K,
    value: RuntimeUpstream[K],
  ) => onChange({ [key]: value });
  return (
    <fieldset className="runtime-upstream">
      <legend>上游 {index + 1}</legend>
      <div className="runtime-upstream-main">
        <Field label="上游地址">
          <input
            aria-label={`上游 ${index + 1} 地址`}
            value={upstream.addr}
            disabled={disabled}
            placeholder="https://dns.example/dns-query"
            required
            aria-invalid={!upstream.addr.trim() || undefined}
            onChange={(event) => update("addr", event.target.value)}
          />
        </Field>
        <button
          type="button"
          className="danger"
          disabled={disabled || !removable}
          onClick={onRemove}
        >
          删除
        </button>
      </div>
      <details>
        <summary>高级连接选项</summary>
        <div className="form-grid">
          <Field label="拨号地址" hint="可选，用于覆盖连接目标。">
            <input
              value={upstream.dial_addr ?? ""}
              disabled={disabled}
              onChange={(event) => update("dial_addr", event.target.value)}
            />
          </Field>
          <Field label="Bootstrap">
            <input
              value={upstream.bootstrap ?? ""}
              disabled={disabled}
              onChange={(event) => update("bootstrap", event.target.value)}
            />
          </Field>
          <Field label="绑定网卡">
            <input
              value={upstream.bind_to_device ?? ""}
              disabled={disabled}
              onChange={(event) => update("bind_to_device", event.target.value)}
            />
          </Field>
          <RuntimeNumberField
            label="SO_MARK"
            value={upstream.so_mark ?? 0}
            min={0}
            max={2147483647}
            disabled={disabled}
            onChange={(value) => update("so_mark", value)}
          />
          <RuntimeNumberField
            label="空闲超时（秒）"
            value={upstream.idle_timeout ?? 0}
            min={0}
            max={2147483647}
            disabled={disabled}
            onChange={(value) => update("idle_timeout", value)}
          />
          <RuntimeNumberField
            label="最大连接数"
            value={upstream.max_conns ?? 0}
            min={0}
            max={2147483647}
            disabled={disabled}
            onChange={(value) => update("max_conns", value)}
          />
        </div>
        <div className="runtime-switches">
          <RuntimeSwitch
            label="信任上游应答"
            checked={Boolean(upstream.trusted)}
            disabled={disabled}
            onChange={(value) => update("trusted", value)}
          />
          <RuntimeSwitch
            label="启用连接复用"
            checked={Boolean(upstream.enable_pipeline)}
            disabled={disabled}
            onChange={(value) => update("enable_pipeline", value)}
          />
          <RuntimeSwitch
            label="跳过 TLS 证书校验"
            checked={Boolean(upstream.insecure)}
            disabled={disabled}
            onChange={(value) => update("insecure", value)}
          />
          <RuntimeSwitch
            label="启用内核 TX"
            checked={Boolean(upstream.kernel_tx)}
            disabled={disabled}
            onChange={(value) => update("kernel_tx", value)}
          />
          <RuntimeSwitch
            label="启用内核 RX"
            checked={Boolean(upstream.kernel_rx)}
            disabled={disabled}
            onChange={(value) => update("kernel_rx", value)}
          />
        </div>
      </details>
    </fieldset>
  );
}

function RuntimeForwardEditor({
  plugin,
  disabled,
  onChange,
  onProbe,
  probes,
  probing,
}: {
  plugin: RuntimePlugin;
  disabled: boolean;
  onChange: (forward: RuntimeFastForward) => void;
  onProbe: () => void;
  probes?: RuntimeProbe[];
  probing: boolean;
}) {
  const forward = plugin.fast_forward;
  if (!forward) return null;
  const update = (index: number, patch: Partial<RuntimeUpstream>) => {
    const upstreams = forward.upstreams.map((item, current) =>
      current === index ? { ...item, ...patch } : item,
    );
    onChange({ upstreams });
  };
  return (
    <Card title={`上游 · ${plugin.tag}`} className="runtime-plugin-card">
      <p className="caption">
        仅显示并编辑已通过安全检查的连接字段。探测结果不包含上游地址。
      </p>
      {forward.upstreams.map((upstream, index) => (
        <RuntimeUpstreamEditor
          key={index}
          upstream={upstream}
          index={index}
          disabled={disabled}
          removable={forward.upstreams.length > 1}
          onChange={(patch) => update(index, patch)}
          onRemove={() =>
            onChange({
              upstreams: forward.upstreams.filter(
                (_, current) => current !== index,
              ),
            })
          }
        />
      ))}
      <div className="actions runtime-plugin-actions">
        <button
          type="button"
          disabled={disabled}
          onClick={() =>
            onChange({ upstreams: [...forward.upstreams, { addr: "" }] })
          }
        >
          添加上游
        </button>
        <button type="button" disabled={disabled || probing} onClick={onProbe}>
          {probing ? "探测中…" : "探测上游"}
        </button>
      </div>
      {probes?.length ? <RuntimeProbeResults probes={probes} /> : null}
    </Card>
  );
}

function RuntimeProbeResults({ probes }: { probes: RuntimeProbe[] }) {
  return (
    <div className="runtime-probes" aria-label="上游探测结果">
      {probes.map((probe) => (
        <div key={probe.upstream_id}>
          <strong>{probe.upstream_id}</strong>
          <span className={probe.success ? "badge ok" : "badge error"}>
            {probe.success ? "成功" : "失败"}
          </span>
          <small>
            {probe.duration_ms.toFixed(1)} ms ·{" "}
            {probe.rcode >= 0 ? `RCODE ${probe.rcode}` : "未收到 DNS 应答"}
          </small>
        </div>
      ))}
    </div>
  );
}

function RuntimeCacheEditor({
  plugin,
  disabled,
  onChange,
}: {
  plugin: RuntimePlugin;
  disabled: boolean;
  onChange: (cache: RuntimeCache) => void;
}) {
  const cache = plugin.cache;
  if (!cache) return null;
  const update = <K extends keyof RuntimeCache>(
    key: K,
    value: RuntimeCache[K],
  ) => onChange(updateValue(cache, key, value));
  return (
    <Card title={`内存缓存 · ${plugin.tag}`} className="runtime-plugin-card">
      <div className="form-grid">
        <RuntimeNumberField
          label="缓存容量"
          value={cache.size}
          min={0}
          max={2147483647}
          disabled={disabled}
          onChange={(value) => update("size", value)}
        />
        <RuntimeNumberField
          label="懒缓存 TTL（秒）"
          value={cache.lazy_cache_ttl}
          min={0}
          max={2147483647}
          disabled={disabled}
          onChange={(value) => update("lazy_cache_ttl", value)}
        />
        <RuntimeNumberField
          label="懒缓存应答 TTL（秒）"
          value={cache.lazy_cache_reply_ttl}
          min={0}
          max={2147483647}
          disabled={disabled}
          onChange={(value) => update("lazy_cache_reply_ttl", value)}
        />
      </div>
      <RuntimeSwitch
        label="压缩 DNS 应答"
        checked={cache.compress_resp}
        disabled={disabled}
        onChange={(value) => update("compress_resp", value)}
      />
    </Card>
  );
}

function RuntimeReadonlyPlugin({ plugin }: { plugin: RuntimePlugin }) {
  const reason =
    readonlyReason[plugin.read_only_reason ?? ""] ??
    "此插件当前只能查看标签和类型。";
  return (
    <li>
      <strong>{plugin.tag}</strong>
      <span>{plugin.type}</span>
      <small>{reason}</small>
    </li>
  );
}

function runtimeSourceStatus(status: string) {
  return (
    {
      available: "可读取",
      last_loaded: "最近一次加载时已读取",
      unavailable: "暂不可用",
      missing: "未配置",
    }[status] ?? status
  );
}

function runtimeCapabilityReason(reason?: string) {
  return (
    {
      managed_config_not_configured: "未配置托管文件，仅支持查看。",
      configured_in_main_config: "由主配置管理，修改后需要重启。",
      sensitive_parameters: "包含敏感连接参数，仅展示安全摘要。",
      unsupported_parameters: "包含当前面板不支持的参数。",
      unsupported_type: "当前插件类型只支持查看。",
    }[reason ?? ""] ??
    reason ??
    "未提供额外说明。"
  );
}

function RuntimeChangePreview({
  baseline,
  draft,
}: {
  baseline: RuntimeConfig;
  draft: RuntimeConfig;
}) {
  const changes = useMemo(() => {
    const items: string[] = [];
    if (baseline.query_log !== draft.query_log)
      items.push(draft.query_log ? "已启用查询明细记录" : "已停用查询明细记录");
    const telemetry: Array<[keyof RuntimeTelemetry, string]> = [
      ["aggregate_retention_days", "聚合数据保留期"],
      ["query_retention_hours", "查询明细保留期"],
      ["max_query_records", "查询明细条数上限"],
    ];
    for (const [key, name] of telemetry) {
      if (baseline.telemetry[key] !== draft.telemetry[key])
        items.push(
          `${name}：${baseline.telemetry[key]} → ${draft.telemetry[key]}`,
        );
    }
    const basePlugins = new Map(
      baseline.plugins.map((plugin) => [plugin.tag, plugin]),
    );
    for (const plugin of draft.plugins) {
      const original = basePlugins.get(plugin.tag);
      if (!original || !plugin.editable) continue;
      if (plugin.type === "fast_forward" && plugin.fast_forward) {
        if (
          JSON.stringify(original.fast_forward) !==
          JSON.stringify(plugin.fast_forward)
        )
          items.push(`上游插件 ${plugin.tag} 的连接参数已修改`);
      }
      if (plugin.type === "cache" && plugin.cache) {
        if (JSON.stringify(original.cache) !== JSON.stringify(plugin.cache))
          items.push(`缓存插件 ${plugin.tag} 的参数已修改`);
      }
    }
    return items;
  }, [baseline, draft]);
  return (
    <Card title="修改预览" className="runtime-preview">
      {changes.length ? (
        <ul>
          {changes.map((change) => (
            <li key={change}>{change}</li>
          ))}
        </ul>
      ) : (
        <p className="caption">尚未修改可托管的运行参数。</p>
      )}
    </Card>
  );
}

function RuntimeHistory({
  items,
  revision,
  disabled,
  loading,
  error,
  onRetry,
  onRollback,
}: {
  items: RuntimeRevision[];
  revision: string;
  disabled: boolean;
  loading: boolean;
  error: string;
  onRetry: () => void;
  onRollback: (target: string) => void;
}) {
  return (
    <Card title="修订历史" className="runtime-history">
      <p className="caption">
        最多保存 10 个旧修订。具备托管能力且当前摘要可用时，可以回滚到其他版本。
      </p>
      {loading ? <Spinner /> : null}
      {error ? (
        <div className="runtime-section-error" aria-live="polite">
          <Alert error={error} />
          <button type="button" onClick={onRetry}>
            重试历史
          </button>
        </div>
      ) : null}
      {!loading && !error && items.length ? (
        <div className="rows">
          {items.map((item) => (
            <div key={item.revision}>
              <span>
                <code>{item.revision.slice(0, 12)}</code>
                <small>{fmt.date(item.created_at)}</small>
              </span>
              {item.revision === revision ? (
                <span className="badge ok">当前</span>
              ) : (
                <button
                  type="button"
                  disabled={disabled}
                  onClick={() => onRollback(item.revision)}
                >
                  回滚
                </button>
              )}
            </div>
          ))}
        </div>
      ) : !loading && !error ? (
        <Empty>尚无可回滚的历史修订</Empty>
      ) : null}
    </Card>
  );
}

export function RuntimeConfigPage() {
  const [configVersion, setConfigVersion] = useState(0);
  const [historyVersion, setHistoryVersion] = useState(0);
  const loadConfig = useCallback(
    (signal: AbortSignal) =>
      request<RuntimeState>("/admin/runtime/config", { signal }),
    [configVersion],
  );
  const loadHistoryRequest = useCallback(
    (signal: AbortSignal) =>
      request<{ items: RuntimeRevision[] }>("/admin/runtime/history", {
        signal,
      }),
    [historyVersion],
  );
  const {
    data: state,
    error: loadError,
    loading,
    setData: setState,
  } = useLoad(loadConfig, [loadConfig]);
  const {
    data: historyData,
    error: historyError,
    loading: historyLoading,
  } = useLoad(loadHistoryRequest, [loadHistoryRequest]);
  const [draft, setDraft] = useState<RuntimeConfig>();
  const [validation, setValidation] = useState<RuntimeValidation>();
  const [probes, setProbes] = useState<Record<string, RuntimeProbe[]>>({});
  const [probeErrors, setProbeErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const preserveDraft = useRef(false);

  useEffect(() => {
    if (!state) return;
    if (preserveDraft.current) {
      preserveDraft.current = false;
      setValidation(undefined);
      return;
    }
    const running = state.sources?.running.config ?? state.config;
    setDraft(copyConfig(running));
    setValidation(undefined);
  }, [state]);

  const runningConfig = state?.sources?.running.config ?? state?.config;
  const hasChanges =
    runningConfig && draft
      ? JSON.stringify(runningConfig) !== JSON.stringify(draft)
      : false;
  const capabilities = state?.capabilities;
  const canManage = state?.mode !== "read_only" && (capabilities?.edit ?? true);
  unstable_usePrompt({
    when: Boolean(hasChanges),
    message: "运行配置还有未保存的修改，确定离开当前页面吗？",
  });
  const confirmLogout = useCallback(
    () =>
      !hasChanges ||
      window.confirm("运行配置还有未保存的修改，确定退出登录吗？"),
    [hasChanges],
  );
  useUnsavedGuard(confirmLogout);

  function confirmReplaceDraft() {
    return (
      !hasChanges ||
      window.confirm("运行配置还有未保存的修改，确定放弃当前修改吗？")
    );
  }

  useEffect(() => {
    if (!hasChanges) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = true;
    };
    window.addEventListener("beforeunload", warn);
    return () => {
      window.removeEventListener("beforeunload", warn);
    };
  }, [hasChanges]);

  function change(next: RuntimeConfig) {
    setDraft(next);
    setValidation(undefined);
    setNotice("");
  }

  function updatePlugin(
    tag: string,
    update: (plugin: RuntimePlugin) => RuntimePlugin,
  ) {
    if (!draft) return;
    change({
      ...draft,
      plugins: draft.plugins.map((plugin) =>
        plugin.tag === tag ? update(plugin) : plugin,
      ),
    });
  }

  async function validate() {
    if (!state || !draft) return;
    const draftError = runtimeDraftError(draft);
    if (draftError) {
      setError(draftError);
      return;
    }
    setBusy("validate");
    setError("");
    setNotice("");
    try {
      const result = await request<RuntimeValidation>(
        "/admin/runtime/config/validate",
        json("POST", { revision: state.revision, config: draft }),
      );
      setValidation(result);
      setNotice(
        result.will_clear_caches
          ? "验证通过。应用后会清空 DNS 缓存。"
          : "验证通过。可以应用本次修改。",
      );
    } catch (reason) {
      setValidation(undefined);
      let errorMessage = message(reason);
      if (reason instanceof APIError && reason.code === "revision_conflict") {
        try {
          const latest = await request<RuntimeState>("/admin/runtime/config");
          preserveDraft.current = true;
          setState(latest);
          setNotice(
            "已载入最新运行基线，并保留你的草稿；请核对差异后重新验证。",
          );
        } catch (reloadReason) {
          errorMessage += ` 最新运行基线加载失败：${message(reloadReason)}；本地草稿仍保留。`;
        }
      }
      setError(errorMessage);
    } finally {
      setBusy("");
    }
  }

  async function apply() {
    if (!validation) return;
    setBusy("apply");
    setError("");
    setNotice("");
    try {
      const result = await request<RuntimeApplyResult>(
        "/admin/runtime/config/apply",
        json("POST", { token: validation.token }),
      );
      setState(result);
      setNotice(
        result.caches_cleared
          ? "运行配置已应用，DNS 缓存已清空。"
          : "运行配置已应用。",
      );
      setHistoryVersion((current) => current + 1);
    } catch (reason) {
      setValidation(undefined);
      let errorMessage = message(reason);
      if (reason instanceof APIError && reason.code === "revision_conflict") {
        try {
          const latest = await request<RuntimeState>("/admin/runtime/config");
          preserveDraft.current = true;
          setState(latest);
          setNotice(
            "已载入最新运行基线，并保留你的草稿；请核对差异后重新验证。",
          );
        } catch (reloadReason) {
          errorMessage += ` 最新运行基线加载失败：${message(reloadReason)}；本地草稿仍保留。`;
        }
      }
      setError(errorMessage);
    } finally {
      setBusy("");
    }
  }

  async function reload() {
    if (!confirmReplaceDraft()) return;
    setBusy("reload");
    setError("");
    setNotice("");
    try {
      const result = await request<RuntimeReloadResult>(
        "/admin/runtime/config/reload",
        { method: "POST" },
      );
      setState(result);
      if (result.restart_required.length) {
        setNotice(
          `主配置包含需要重启的项：${result.restart_required.join("、")}。当前运行配置未切换。`,
        );
      } else {
        setNotice(
          result.caches_cleared
            ? "主配置已重读，DNS 缓存已清空。"
            : "主配置已重读。",
        );
      }
      setHistoryVersion((current) => current + 1);
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy("");
    }
  }

  async function rollback(targetRevision: string) {
    if (!state) return;
    if (!confirmReplaceDraft()) return;
    setBusy(`rollback:${targetRevision}`);
    setError("");
    setNotice("");
    try {
      const result = await request<RuntimeApplyResult>(
        "/admin/runtime/rollback",
        json("POST", {
          revision: state.revision,
          target_revision: targetRevision,
        }),
      );
      setState(result);
      setNotice(
        result.caches_cleared
          ? "已回滚运行配置，DNS 缓存已清空。"
          : "已回滚运行配置。",
      );
      setHistoryVersion((current) => current + 1);
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy("");
    }
  }

  async function probe(tag: string) {
    setBusy(`probe:${tag}`);
    setError("");
    setProbeErrors((current) => ({ ...current, [tag]: "" }));
    try {
      const result = await request<{ items: RuntimeProbe[] }>(
        `/admin/runtime/upstreams/${encodeURIComponent(tag)}/probe`,
        { method: "POST" },
      );
      setProbes((current) => ({ ...current, [tag]: result.items }));
    } catch (reason) {
      setProbeErrors((current) => ({ ...current, [tag]: message(reason) }));
    } finally {
      setBusy("");
    }
  }

  return (
    <>
      <PageTitle
        title="运行配置"
        description="分别查看正在运行的安全摘要，并在启用托管后验证、应用和回滚支持的设置。"
        action={
          <div className="page-actions">
            <button
              type="button"
              disabled={loading}
              onClick={() => {
                if (confirmReplaceDraft()) {
                  setConfigVersion((current) => current + 1);
                }
              }}
            >
              重新读取摘要
            </button>
            {state && (capabilities?.reload ?? canManage) ? (
              <button
                type="button"
                disabled={Boolean(busy)}
                onClick={() => void reload()}
              >
                {busy === "reload" ? "重读中…" : "重读主配置"}
              </button>
            ) : null}
          </div>
        }
      />
      {loading ? <Spinner /> : null}
      {loadError ? (
        <Card title="运行配置摘要加载失败" className="runtime-section-error">
          <div aria-live="polite">
            <Alert error={loadError} />
          </div>
          <p className="caption">
            加载失败与未配置托管文件是不同状态。请重试以确认当前节点能力。
          </p>
          <button onClick={() => setConfigVersion((current) => current + 1)}>
            重试摘要
          </button>
        </Card>
      ) : null}
      <div aria-live="polite">
        <Alert error={error} />
        {notice ? <div className="notice">{notice}</div> : null}
      </div>
      {state && draft ? (
        <div className="runtime-config">
          {!canManage ? (
            <div className="notice runtime-readonly-notice">
              <strong>当前为只读模式。</strong>
              <span>
                未启用托管配置不会影响 DNS
                服务；当前页面仍展示服务端已脱敏的运行摘要。验证、应用、重读、历史和回滚需按当前版本说明配置托管文件。
              </span>
            </div>
          ) : null}
          <Card title="运行状态" className="runtime-status">
            <div className="rows">
              <div>
                <span>当前修订</span>
                <code>
                  {state.revision ? state.revision.slice(0, 12) : "尚未保存"}
                </code>
              </div>
              <div>
                <span>管理模式</span>
                <strong>{canManage ? "托管管理" : "只读检查"}</strong>
              </div>
              <div>
                <span>可编辑插件</span>
                <strong>
                  {canManage
                    ? draft.plugins.filter((plugin) => plugin.editable).length
                    : 0}{" "}
                  个
                </strong>
              </div>
            </div>
            {state.sources ? (
              <div className="runtime-source-grid">
                <div>
                  <strong>正在运行</strong>
                  <span>
                    {runtimeSourceStatus(state.sources.running.status)}
                  </span>
                  <small>页面编辑基于此运行摘要。</small>
                </div>
                <div>
                  <strong>已加载的主配置基线</strong>
                  <span>{runtimeSourceStatus(state.sources.base.status)}</span>
                  <small>
                    这是最近一次成功加载的副本；磁盘文件之后可能已变化。
                  </small>
                </div>
                <div>
                  <strong>候选配置</strong>
                  <span>
                    {runtimeSourceStatus(state.sources.candidate.status)}
                  </span>
                  <small>未应用候选不会标记为正在运行。</small>
                </div>
              </div>
            ) : null}
          </Card>
          <Card title="查询与统计">
            <RuntimeSwitch
              label="记录查询明细"
              checked={draft.query_log}
              disabled={Boolean(busy) || !canManage}
              onChange={(value) => change({ ...draft, query_log: value })}
            />
            <div className="form-grid">
              <RuntimeNumberField
                label="聚合保留天数"
                value={draft.telemetry.aggregate_retention_days}
                min={1}
                max={31}
                hint="1 至 31 天"
                disabled={Boolean(busy) || !canManage}
                onChange={(value) =>
                  change({
                    ...draft,
                    telemetry: updateValue(
                      draft.telemetry,
                      "aggregate_retention_days",
                      value,
                    ),
                  })
                }
              />
              <RuntimeNumberField
                label="查询明细保留小时"
                value={draft.telemetry.query_retention_hours}
                min={1}
                max={720}
                hint="1 至 720 小时"
                disabled={Boolean(busy) || !canManage}
                onChange={(value) =>
                  change({
                    ...draft,
                    telemetry: updateValue(
                      draft.telemetry,
                      "query_retention_hours",
                      value,
                    ),
                  })
                }
              />
              <RuntimeNumberField
                label="查询明细条数上限"
                value={draft.telemetry.max_query_records}
                min={1000}
                max={5000000}
                hint="1000 至 5000000 条"
                disabled={Boolean(busy) || !canManage}
                onChange={(value) =>
                  change({
                    ...draft,
                    telemetry: updateValue(
                      draft.telemetry,
                      "max_query_records",
                      value,
                    ),
                  })
                }
              />
            </div>
          </Card>
          {draft.plugins
            .filter(
              (plugin) => plugin.editable && plugin.type === "fast_forward",
            )
            .map((plugin) => (
              <section className="runtime-plugin-stack" key={plugin.tag}>
                <RuntimeForwardEditor
                  plugin={plugin}
                  disabled={Boolean(busy) || !canManage}
                  probes={probes[plugin.tag]}
                  probing={busy === `probe:${plugin.tag}`}
                  onProbe={() => void probe(plugin.tag)}
                  onChange={(fast_forward) =>
                    updatePlugin(plugin.tag, (current) => ({
                      ...current,
                      fast_forward,
                    }))
                  }
                />
                {probeErrors[plugin.tag] ? (
                  <div className="runtime-section-error" aria-live="polite">
                    <Alert error={probeErrors[plugin.tag]} />
                    <button
                      type="button"
                      onClick={() => void probe(plugin.tag)}
                    >
                      重试探测
                    </button>
                  </div>
                ) : null}
              </section>
            ))}
          {draft.plugins
            .filter((plugin) => plugin.editable && plugin.type === "cache")
            .map((plugin) => (
              <RuntimeCacheEditor
                key={plugin.tag}
                plugin={plugin}
                disabled={Boolean(busy) || !canManage}
                onChange={(cache) =>
                  updatePlugin(plugin.tag, (current) => ({ ...current, cache }))
                }
              />
            ))}
          <RuntimeChangePreview
            baseline={runningConfig ?? state.config}
            draft={draft}
          />
          {canManage ? (
            <Card title="应用配置" className="runtime-apply">
              <p className="caption">
                验证会完整构建候选运行代，但不会切换 DNS
                服务。验证令牌仅在当前管理员会话中有效 5 分钟，且只能应用一次。
              </p>
              <div className="actions">
                <button
                  type="button"
                  disabled={Boolean(busy) || !hasChanges}
                  onClick={() => {
                    setDraft(copyConfig(runningConfig ?? state.config));
                    setValidation(undefined);
                    setNotice("");
                    setError("");
                  }}
                >
                  放弃修改
                </button>
                <button
                  type="button"
                  className="primary"
                  disabled={Boolean(busy) || !hasChanges}
                  onClick={() => void validate()}
                >
                  {busy === "validate" ? "验证中…" : "验证修改"}
                </button>
                <button
                  type="button"
                  disabled={Boolean(busy) || !validation}
                  onClick={() => void apply()}
                >
                  {busy === "apply" ? "应用中…" : "应用已验证的修改"}
                </button>
              </div>
              {validation ? (
                <small className="runtime-validation">
                  验证令牌有效至 {fmt.date(validation.expires_at)}。
                </small>
              ) : null}
            </Card>
          ) : null}
          {state.sources?.running.data_providers?.length ? (
            <Card title="节点数据源" className="runtime-data-providers">
              <p className="caption">
                这些数据源来自主配置并参与既有分流。运行时暂不提供的条目数和加载时间不会推测为
                0。
              </p>
              <div className="rows">
                {state.sources.running.data_providers.map((provider) => (
                  <div key={provider.tag}>
                    <span>
                      <strong>{provider.tag}</strong>
                      <small>{provider.file || "来源暂不可用"}</small>
                    </span>
                    <span>
                      {provider.auto_reload ? "自动重载" : "不自动重载"}
                      <small>
                        条目：
                        {provider.runtime_state?.entry_count == null
                          ? "暂不可用"
                          : fmt.num(provider.runtime_state.entry_count)}{" "}
                        · 最近加载：
                        {provider.runtime_state?.loaded_at
                          ? fmt.date(provider.runtime_state.loaded_at)
                          : "暂不可用"}
                      </small>
                    </span>
                  </div>
                ))}
              </div>
            </Card>
          ) : null}
          {state.sources?.running.plugins?.length ? (
            <Card title="插件能力" className="runtime-capabilities">
              <div className="runtime-capability-list">
                {state.sources.running.plugins.map((plugin) => (
                  <div key={plugin.tag}>
                    <span>
                      <strong>{plugin.tag}</strong>
                      <small>{plugin.type}</small>
                    </span>
                    <span>
                      {plugin.capability.edit ? "可编辑" : "只读"} ·{" "}
                      {plugin.capability.hot_reload
                        ? "可热更新"
                        : plugin.capability.restart_required
                          ? "修改需重启"
                          : "不可热更新"}
                    </span>
                    <small>
                      {plugin.capability.reason
                        ? runtimeCapabilityReason(plugin.capability.reason)
                        : plugin.capability.clears_memory_caches
                          ? "应用后清空内存缓存"
                          : "应用不清空内存缓存"}
                    </small>
                    {plugin.safe_args ? (
                      <details>
                        <summary>查看安全摘要</summary>
                        <pre>{JSON.stringify(plugin.safe_args, null, 2)}</pre>
                      </details>
                    ) : null}
                  </div>
                ))}
              </div>
            </Card>
          ) : null}
          <Card title="只读插件" className="runtime-readonly">
            {draft.plugins.filter((plugin) => !plugin.editable).length ? (
              <ul>
                {draft.plugins
                  .filter((plugin) => !plugin.editable)
                  .map((plugin) => (
                    <RuntimeReadonlyPlugin key={plugin.tag} plugin={plugin} />
                  ))}
              </ul>
            ) : (
              <Empty>当前配置没有只读插件</Empty>
            )}
          </Card>
        </div>
      ) : null}
      <RuntimeHistory
        items={historyData?.items ?? []}
        revision={state?.revision ?? ""}
        disabled={
          Boolean(busy) || !state || !(capabilities?.rollback ?? canManage)
        }
        loading={historyLoading}
        error={historyError}
        onRetry={() => setHistoryVersion((current) => current + 1)}
        onRollback={(target) => void rollback(target)}
      />
    </>
  );
}
