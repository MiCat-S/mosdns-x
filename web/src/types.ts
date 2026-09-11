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
