import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { message } from "./api";
import { useSession } from "./session";
const UsageChart = lazy(() => import("./usage-chart"));

export function Spinner() {
  return (
    <div className="state" role="status">
      <span className="spinner" />
      正在加载…
    </div>
  );
}
export function Alert({ error }: { error: string }) {
  return error ? (
    <div className="alert" role="alert">
      {error}
    </div>
  ) : null;
}
export function Empty({ children = "暂无数据" }: { children?: ReactNode }) {
  return <div className="empty">{children}</div>;
}
export function PageTitle({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <header className="page-title">
      <div>
        <h1>{title}</h1>
        {description ? <p>{description}</p> : null}
      </div>
      {action}
    </header>
  );
}
export function Metric({
  label,
  value,
  hint,
}: {
  label: string;
  value: ReactNode;
  hint?: string;
}) {
  return (
    <article className="metric">
      <span>{label}</span>
      <strong>{value}</strong>
      {hint ? <small>{hint}</small> : null}
    </article>
  );
}
export function Card({
  title,
  children,
  className = "",
}: {
  title?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`card ${className}`}>
      {title ? <h2>{title}</h2> : null}
      {children}
    </section>
  );
}
export function Chart({
  series,
  from,
  to,
  kind = "results",
}: {
  series: {
    time: string;
    completed: number;
    failed: number;
    cache_hits: number;
  }[];
  from: string;
  to: string;
  kind?: "results" | "usage";
}) {
  return (
    <Suspense fallback={<Spinner />}>
      <UsageChart series={series} from={from} to={to} kind={kind} />
    </Suspense>
  );
}
export function useLoad<T>(
  loader: (signal: AbortSignal) => Promise<T>,
  deps: unknown[] = [],
) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const generation = useRef(0);
  useEffect(() => {
    const current = ++generation.current;
    const c = new AbortController();
    setData(undefined);
    setLoading(true);
    setError("");
    loader(c.signal)
      .then((value) => {
        if (!c.signal.aborted && generation.current === current) setData(value);
      })
      .catch((e) => {
        if (!c.signal.aborted && generation.current === current)
          setError(message(e));
      })
      .finally(() => {
        if (!c.signal.aborted && generation.current === current)
          setLoading(false);
      });
    return () => c.abort();
  }, deps);
  return { data, error, loading, setData };
}

type NavIconName =
  | "overview"
  | "users"
  | "audit"
  | "system"
  | "usage"
  | "credential"
  | "password";
type NavItem = { to: string; label: string; icon: NavIconName };

const adminNav: NavItem[] = [
  { to: "/admin", label: "总览", icon: "overview" },
  { to: "/admin/users", label: "用户", icon: "users" },
  { to: "/admin/audit", label: "审计", icon: "audit" },
  { to: "/admin/system", label: "系统", icon: "system" },
];
const userNav: NavItem[] = [
  { to: "/app", label: "我的服务", icon: "overview" },
  { to: "/app/usage", label: "用量", icon: "usage" },
  { to: "/app/credentials", label: "凭证", icon: "credential" },
  { to: "/app/password", label: "密码", icon: "password" },
];

function NavIcon({ name }: { name: NavIconName }) {
  const paths: Record<NavIconName, ReactNode> = {
    overview: (
      <>
        <path d="M4 13h6V4H4v9Zm0 7h6v-4H4v4Zm10 0h6v-9h-6v9Zm0-16v4h6V4h-6Z" />
      </>
    ),
    users: (
      <>
        <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
        <circle cx="9" cy="7" r="4" />
        <path d="M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" />
      </>
    ),
    audit: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M12 7v5l3 2" />
      </>
    ),
    system: (
      <>
        <circle cx="12" cy="12" r="3" />
        <path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.55V21h-4v-.08A1.7 1.7 0 0 0 8.94 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.57 15 1.7 1.7 0 0 0 3 14H3v-4h.08A1.7 1.7 0 0 0 4.6 8.94a1.7 1.7 0 0 0-.34-1.88L4.2 7l2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.57 1.7 1.7 0 0 0 10 3.08V3h4v.08A1.7 1.7 0 0 0 15.06 4.6a1.7 1.7 0 0 0 1.88-.34L17 4.2 19.83 7l-.06.06A1.7 1.7 0 0 0 19.43 9 1.7 1.7 0 0 0 21 10h.08v4H21a1.7 1.7 0 0 0-1.6 1Z" />
      </>
    ),
    usage: (
      <>
        <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />
      </>
    ),
    credential: (
      <>
        <circle cx="8" cy="15" r="4" />
        <path d="m11 12 8-8 2 2-2 2 2 2-3 3-2-2-2 2" />
      </>
    ),
    password: (
      <>
        <rect x="4" y="10" width="16" height="11" rx="2" />
        <path d="M8 10V7a4 4 0 0 1 8 0v3M12 15v2" />
      </>
    ),
  };
  return (
    <svg aria-hidden viewBox="0 0 24 24">
      {paths[name]}
    </svg>
  );
}
export function Shell() {
  const { session, logout } = useSession();
  const navigate = useNavigate();
  const [logoutError, setLogoutError] = useState("");
  const admin = session?.user.role === "admin";
  const links = admin
    ? [
        ...adminNav,
        { to: "/admin/password", label: "密码", icon: "password" as const },
      ]
    : userNav;
  async function exit() {
    setLogoutError("");
    try {
      await logout();
      navigate("/login");
    } catch (e) {
      setLogoutError(message(e));
    }
  }
  return (
    <div className="shell">
      <aside>
        <NavLink className="brand" to={admin ? "/admin" : "/app"}>
          <span className="brandmark">M</span>
          <span className="brand-copy">
            <strong>MosDNS</strong>
            <small>Control Center</small>
          </span>
        </NavLink>
        <span className="nav-caption">{admin ? "管理控制台" : "用户中心"}</span>
        <nav aria-label="主导航">
          {links.map(({ to, label, icon }, i) => (
            <NavLink key={to} to={to} end={i === 0}>
              <i>
                <NavIcon name={icon} />
              </i>
              <span>{label}</span>
            </NavLink>
          ))}
        </nav>
        <div className="account">
          <span className="account-avatar" aria-hidden>
            {session?.user.username.slice(0, 1).toUpperCase()}
          </span>
          <span className="account-copy">
            <strong>{session?.user.username}</strong>
            <small>{admin ? "管理员" : "用户"}</small>
          </span>
          <button className="link" onClick={exit}>
            退出登录
          </button>
        </div>
      </aside>
      <main>
        {logoutError ? <Alert error={logoutError} /> : null}
        <Outlet />
      </main>
    </div>
  );
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {hint ? <small>{hint}</small> : null}
    </label>
  );
}
export function Modal({
  title,
  children,
  onClose,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    dialog.showModal();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className="modal"
      aria-labelledby="modal-title"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div onClick={(e) => e.stopPropagation()}>
        <header>
          <h2 id="modal-title">{title}</h2>
          <button className="icon-button" onClick={onClose} aria-label="关闭">
            ×
          </button>
        </header>
        {children}
      </div>
    </dialog>
  );
}
export const fmt = {
  num: (n: number) => new Intl.NumberFormat("zh-CN").format(n),
  date: (v: string) =>
    !v || v.startsWith("0001-01-01")
      ? "未设置"
      : new Intl.DateTimeFormat("zh-CN", {
          dateStyle: "medium",
          timeStyle: "short",
        }).format(new Date(v)),
  pct: (n: number, d: number) =>
    d ? `${Math.min(100, (n / d) * 100).toFixed(1)}%` : "0%",
};
