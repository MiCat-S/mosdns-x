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
export interface UserSettings {
  user_id: string;
  strip_ecs: boolean;
  block_private_answers: boolean;
  blocked_qtypes: string[];
  custom_block_enabled: boolean;
  custom_allow_enabled: boolean;
  custom_rewrite_enabled: boolean;
  policy_paused_until: string | null;
  updated_at: string;
}
export type RuleAction = "allow" | "block" | "rewrite";
export type RuleMatch = "exact" | "suffix" | "keyword" | "regexp";
export type RuleRecordType = "A" | "AAAA" | "CNAME";
export interface Rule {
  id: string;
  user_id: string;
  priority: number;
  action: RuleAction;
  match: RuleMatch;
  pattern: string;
  record_type?: RuleRecordType;
  value?: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}
export interface LookupQuestion {
  name: string;
  qtype: string;
}
export interface LookupRecord {
  name?: string;
  type?: string;
  ttl?: number;
  value?: string;
  data?: string;
}
export interface LookupResult {
  question: LookupQuestion;
  rcode: string;
  duration_ms: number;
  answers: LookupRecord[];
  authority: LookupRecord[];
  additional: LookupRecord[];
  edns?: EDNSInfo;
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
export type PublicListFormat = "mosdns" | "hosts";
export interface PublicList {
  id: string;
  name: string;
  category?: string;
  url: string;
  format: PublicListFormat;
  enabled: boolean;
  sha256?: string;
  refresh_seconds: number;
  entry_count?: number;
  last_refresh_status?: string;
  last_refreshed_at?: string;
  last_refresh_error?: string;
  created_at: string;
  updated_at: string;
}
export interface UserPublicList {
  list: PublicList;
  enabled: boolean;
  overridden: boolean;
}
export interface RuntimeTelemetry {
  aggregate_retention_days: number;
  query_retention_hours: number;
  max_query_records: number;
}
export interface RuntimeUpstream {
  addr: string;
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
  response_source?: string;
  response_source_id?: string;
  upstream_id?: string;
  matched_rule_id?: string;
  matched_public_list_id?: string;
}
export interface EDNSInfo {
  present: boolean;
  version: number;
  udp_size: number;
  dnssec_ok: boolean;
  option_codes: number[];
  ecs?: ECSInfo;
}
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
