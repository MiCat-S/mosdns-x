import { useCallback, useEffect, useMemo, useState } from "react";
import { json, message, request } from "./api";
import {
  Alert,
  Card,
  Empty,
  Field,
  PageTitle,
  Spinner,
  fmt,
  useLoad,
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
  return (
    <Field label={label} hint={hint}>
      <input
        type="number"
        min={min}
        max={max}
        value={value}
        disabled={disabled}
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
  onRollback,
}: {
  items: RuntimeRevision[];
  revision: string;
  disabled: boolean;
  onRollback: (target: string) => void;
}) {
  return (
    <Card title="修订历史" className="runtime-history">
      <p className="caption">
        最多保存 10 个旧修订。当前修订以外的版本可直接回滚。
      </p>
      {items.length ? (
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
      ) : (
        <Empty>尚无可回滚的历史修订</Empty>
      )}
    </Card>
  );
}

export function RuntimeConfigPage() {
  const load = useCallback(async (signal: AbortSignal) => {
    const [state, history] = await Promise.all([
      request<RuntimeState>("/admin/runtime/config", { signal }),
      request<{ items: RuntimeRevision[] }>("/admin/runtime/history", {
        signal,
      }),
    ]);
    return { state, history: history.items };
  }, []);
  const { data, error: loadError, loading, setData } = useLoad(load, [load]);
  const [draft, setDraft] = useState<RuntimeConfig>();
  const [validation, setValidation] = useState<RuntimeValidation>();
  const [history, setHistory] = useState<RuntimeRevision[]>([]);
  const [probes, setProbes] = useState<Record<string, RuntimeProbe[]>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    if (!data) return;
    setDraft(copyConfig(data.state.config));
    setValidation(undefined);
    setHistory(data.history);
  }, [data]);

  const state = data?.state;
  const hasChanges =
    state && draft
      ? JSON.stringify(state.config) !== JSON.stringify(draft)
      : false;

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

  async function loadHistory() {
    const result = await request<{ items: RuntimeRevision[] }>(
      "/admin/runtime/history",
    );
    setHistory(result.items);
  }

  async function validate() {
    if (!state || !draft) return;
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
      setError(message(reason));
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
      setData((previous) =>
        previous
          ? {
              ...previous,
              state: { revision: result.revision, config: result.config },
            }
          : previous,
      );
      setNotice(
        result.caches_cleared
          ? "运行配置已应用，DNS 缓存已清空。"
          : "运行配置已应用。",
      );
      await loadHistory();
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy("");
    }
  }

  async function reload() {
    setBusy("reload");
    setError("");
    setNotice("");
    try {
      const result = await request<RuntimeReloadResult>(
        "/admin/runtime/config/reload",
        { method: "POST" },
      );
      setData((previous) =>
        previous
          ? {
              ...previous,
              state: { revision: result.revision, config: result.config },
            }
          : previous,
      );
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
      await loadHistory();
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy("");
    }
  }

  async function rollback(targetRevision: string) {
    if (!state) return;
    setBusy(`rollback:${targetRevision}`);
    setError("");
    setNotice("");
    try {
      const result = await request<RuntimeApplyResult>(
        "/admin/runtime/config/rollback",
        json("POST", {
          revision: state.revision,
          target_revision: targetRevision,
        }),
      );
      setData((previous) =>
        previous
          ? {
              ...previous,
              state: { revision: result.revision, config: result.config },
            }
          : previous,
      );
      setNotice(
        result.caches_cleared
          ? "已回滚运行配置，DNS 缓存已清空。"
          : "已回滚运行配置。",
      );
      await loadHistory();
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy("");
    }
  }

  async function probe(tag: string) {
    setBusy(`probe:${tag}`);
    setError("");
    try {
      const result = await request<{ items: RuntimeProbe[] }>(
        `/admin/runtime/upstreams/${encodeURIComponent(tag)}/probe`,
        { method: "POST" },
      );
      setProbes((current) => ({ ...current, [tag]: result.items }));
    } catch (reason) {
      setError(message(reason));
    } finally {
      setBusy("");
    }
  }

  return (
    <>
      <PageTitle
        title="运行配置"
        description="在不暴露凭证的前提下，验证并热更新可托管的上游、缓存和统计设置。"
        action={
          <button
            type="button"
            disabled={Boolean(busy)}
            onClick={() => void reload()}
          >
            {busy === "reload" ? "重读中…" : "重读主配置"}
          </button>
        }
      />
      {loading ? <Spinner /> : null}
      <Alert error={loadError || error} />
      {notice ? <div className="notice">{notice}</div> : null}
      {state && draft ? (
        <div className="runtime-config">
          <Card title="运行状态" className="runtime-status">
            <div className="rows">
              <div>
                <span>当前修订</span>
                <code>
                  {state.revision ? state.revision.slice(0, 12) : "尚未保存"}
                </code>
              </div>
              <div>
                <span>可托管插件</span>
                <strong>
                  {draft.plugins.filter((plugin) => plugin.editable).length} 个
                </strong>
              </div>
            </div>
          </Card>
          <Card title="查询与统计">
            <RuntimeSwitch
              label="记录查询明细"
              checked={draft.query_log}
              disabled={Boolean(busy)}
              onChange={(value) => change({ ...draft, query_log: value })}
            />
            <div className="form-grid">
              <RuntimeNumberField
                label="聚合保留天数"
                value={draft.telemetry.aggregate_retention_days}
                min={1}
                max={31}
                hint="1 至 31 天"
                disabled={Boolean(busy)}
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
                disabled={Boolean(busy)}
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
                disabled={Boolean(busy)}
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
              <RuntimeForwardEditor
                key={plugin.tag}
                plugin={plugin}
                disabled={Boolean(busy)}
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
            ))}
          {draft.plugins
            .filter((plugin) => plugin.editable && plugin.type === "cache")
            .map((plugin) => (
              <RuntimeCacheEditor
                key={plugin.tag}
                plugin={plugin}
                disabled={Boolean(busy)}
                onChange={(cache) =>
                  updatePlugin(plugin.tag, (current) => ({ ...current, cache }))
                }
              />
            ))}
          <RuntimeChangePreview baseline={state.config} draft={draft} />
          <Card title="应用配置" className="runtime-apply">
            <p className="caption">
              验证会完整构建候选运行代，但不会切换 DNS
              服务。验证令牌仅在当前管理员会话中有效 5 分钟，且只能应用一次。
            </p>
            <div className="actions">
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
          <RuntimeHistory
            items={history}
            revision={state.revision}
            disabled={Boolean(busy)}
            onRollback={(target) => void rollback(target)}
          />
        </div>
      ) : null}
    </>
  );
}
