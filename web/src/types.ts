export type Role = "admin" | "user";
export type Period = "daily" | "monthly";
export interface User {
  id: string;
  username: string;
  role: Role;
  enabled: boolean;
  expires_at: string;
  period: Period;
  timezone: string;
  limit: number;
  qps: number;
  burst: number;
  max_credentials: number;
  created_at: string;
  updated_at: string;
}
export interface Session {
  user: User;
  csrf_token: string;
  expires_at: string;
}
export interface Quota {
  period: Period;
  timezone: string;
  period_start: string;
  period_end: string;
  limit: number;
  used: number;
  remaining: number;
}
export interface Me {
  user: User;
  quota: Quota;
  public_dns_url: string;
}
export interface Credential {
  id: string;
  user_id: string;
  name: string;
  expires_at: string;
  revoked_at: string;
  created_at: string;
  updated_at: string;
}
export interface IssuedCredential {
  credential: Credential;
  token: string;
  doh_url: string;
}
export interface Page<T> {
  items: T[];
  next_cursor?: string;
}
export interface RuntimeDataProvider {
  tag: string;
  file?: string;
  auto_reload?: boolean;
  declared?: boolean;
  capability?: RuntimeCapability;
  file_state?: {
    status: string;
    size_bytes?: number | null;
    modified_at?: string | null;
  };
  runtime_state?: {
    status: string;
    entry_count?: number | null;
    loaded_at?: string | null;
    reason?: string;
  };
}
export interface RuntimeCapability {
  name?: string;
  view: boolean;
  edit: boolean;
  hot_reload: boolean;
  restart_required: boolean;
  clears_memory_caches: boolean;
  reason?: string;
}
export interface RuntimePluginSummary {
  tag: string;
  type: string;
  safe_args?: Record<string, unknown>;
  capability: RuntimeCapability;
}
export interface RuntimeSource {
  kind: string;
  status: string;
  config?: RuntimeConfig;
  plugins?: RuntimePluginSummary[];
  data_providers?: RuntimeDataProvider[];
  components?: RuntimeCapability[];
}
export interface RuntimeTelemetry {
  aggregate_retention_days: number;
  query_retention_hours: number;
  max_query_records: number;
}
export interface RuntimeUpstream {
  addr: string;
  label?: string;
  dial_addr?: string;
  trusted?: boolean;
  so_mark?: number;
  bind_to_device?: string;
  idle_timeout?: number;
  max_conns?: number;
  enable_pipeline?: boolean;
  bootstrap?: string;
  insecure?: boolean;
  kernel_tx?: boolean;
  kernel_rx?: boolean;
  proxy?: RuntimeProxy;
}
// An upstream's proxy as the server shows it: the key or password is never
// sent back (password_set says one is saved); leaving password empty keeps
// the saved one for the same type, server, method and username.
export interface RuntimeProxy {
  type: "shadowsocks" | "socks5";
  server: string;
  method?: string;
  username?: string;
  password?: string;
  password_set?: boolean;
}
export interface RuntimeFastForward {
  upstreams: RuntimeUpstream[];
}
export interface RuntimeCache {
  size: number;
  lazy_cache_ttl: number;
  lazy_cache_reply_ttl: number;
  compress_resp: boolean;
}
export interface RuntimePlugin {
  tag: string;
  type: string;
  editable: boolean;
  read_only_reason?: string;
  fast_forward?: RuntimeFastForward;
  cache?: RuntimeCache;
}
export interface RuntimeConfig {
  version: number;
  query_log: boolean;
  telemetry: RuntimeTelemetry;
  plugins: RuntimePlugin[];
}
export interface RuntimeState {
  revision: string;
  config: RuntimeConfig;
  mode?: "managed" | "read_only";
  setup?: {
    managed_config_configured: boolean;
    config_source_available: boolean;
    reason?: string;
  };
  capabilities?: {
    view: boolean;
    edit: boolean;
    validate: boolean;
    apply: boolean;
    reload: boolean;
    history: boolean;
    rollback: boolean;
    probe: boolean;
  };
  sources?: {
    running: RuntimeSource;
    base: RuntimeSource;
    candidate: RuntimeSource;
  };
}
export interface RuntimeValidation {
  token: string;
  expires_at: string;
  revision: string;
  will_clear_caches: boolean;
}
export interface RuntimeApplyResult extends RuntimeState {
  caches_cleared: boolean;
}
export interface RuntimeReloadResult extends RuntimeState {
  restart_required: string[];
  caches_cleared: boolean;
}
export interface RuntimeRevision {
  revision: string;
  created_at: string;
}
export interface RuntimeProbe {
  upstream_id: string;
  duration_ms: number;
  rcode: number;
  success: boolean;
  error?: string;
}
export interface UsagePoint {
  minute: string;
  user_id: string;
  credential_id?: string;
  count: number;
}
export type DeviceUsagePoint = UsagePoint;
export interface SeriesPoint {
  time: string;
  completed: number;
  failed: number;
  cache_hits: number;
  avg_latency_ms: number;
}
export interface Upstream {
  id: string;
  attempts: number;
  failures: number;
  avg_latency_ms: number;
}
export interface DomainStats {
  name: string;
  queries: number;
  cache_hits: number;
}
export interface TopDomains {
  from: string;
  to: string;
  queries: number;
  domains: DomainStats[];
  query_log_enabled: boolean;
}
export interface Stats {
  from: string;
  to: string;
  completed: number;
  failed: number;
  cache_hits: number;
  avg_latency_ms: number;
  p95_latency_ms: number;
  rcode_counts: Record<string, number>;
  series: SeriesPoint[];
  upstreams: Upstream[];
  dropped: number;
  updated_at: string;
  query_log_enabled: boolean;
}
export interface QueryRecord {
  id: string;
  time: string;
  user_id: string;
  credential_id: string;
  client_ip: string;
  name: string;
  qtype: string;
  rcode: string;
  duration_ms: number;
  cache_hit: boolean;
  protocol: string;
  answer_ips: string[];
  edns?: EDNSInfo;
  edns_trace_version?: number;
  upstream_stage_status?: UpstreamStageStatus;
  upstream_request_edns?: EDNSInfo | null;
  upstream_response_edns?: EDNSInfo | null;
  response_edns?: EDNSInfo | null;
  response_source?: string;
  response_source_id?: string;
  upstream_id?: string;
  upstream_label?: string;
  trace?: QueryTrace | null;
  // Current names of the record's account and device; absent once either
  // has been deleted.
  username?: string;
  device_name?: string;
}
export interface RouteStep {
  at_ms: number;
  branch?: string;
  kind: string;
  detail?: string;
  hits?: string[];
  matched?: boolean;
  misses?: string[];
  then?: "exec" | "else" | "continue";
}
export interface UpstreamTry {
  seq: number;
  branch?: string;
  plugin?: string;
  upstream: string;
  start_ms: number;
  duration_ms: number;
  done: boolean;
  rcode?: string;
  error?: string;
  selected?: boolean;
}
export interface QueryTrace {
  steps: RouteStep[];
  attempts: UpstreamTry[];
  truncated?: boolean;
}
export type UpstreamStageStatus =
  | "selected"
  | "attempted_no_selection"
  | "discarded"
  | "not_linked"
  | "unavailable";
export interface EDNSInfo {
  present: boolean;
  version: number;
  udp_size: number;
  dnssec_ok: boolean;
  option_codes: number[];
  option_codes_truncated?: boolean;
  ecs?: ECSInfo;
  anomalies?: EDNSAnomaly[];
}
export type EDNSAnomaly =
  | "multiple_opt"
  | "multiple_ecs"
  | "invalid_ecs_family"
  | "invalid_ecs_prefix"
  | "invalid_ecs_address";
export interface ECSInfo {
  address: string;
  family: number;
  source_prefix: number;
  scope_prefix: number;
}
export interface Audit {
  id: string;
  actor_id?: string;
  action: string;
  target_type: string;
  target_id?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}
export interface SystemInfo {
  version: string;
  started_at: string;
  public_dns_url: string;
  query_log_enabled: boolean;
  config: SystemConfig;
}
export interface SystemConfig {
  dns_protocols: string[];
  management_enabled: boolean;
  pprof_enabled: boolean;
  control_storage: string;
  telemetry_storage: string;
}
export interface CachePurgeResult {
  domain: string;
  subdomains: boolean;
  caches: number;
  removed: number;
}
