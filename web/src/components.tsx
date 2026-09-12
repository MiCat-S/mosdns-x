import {
  createContext,
  lazy,
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { message } from "./api";
import { useSession } from "./session";
const UsageChart = lazy(() => import("./usage-chart"));

type UnsavedGuard = () => boolean;
type RegisterUnsavedGuard = (guard: UnsavedGuard) => () => void;
const UnsavedGuardContext = createContext<RegisterUnsavedGuard>(() => () => {});

export function useUnsavedGuard(guard: UnsavedGuard) {
  const register = useContext(UnsavedGuardContext);
  useEffect(() => register(guard), [guard, register]);
}

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
  | "logs"
  | "audit"
  | "system"
  | "usage"
  | "credential"
  | "password"
  | "shield"
  | "rules"
  | "lookup"
  | "account"
  | "help"
  | "list"
  | "lab";
type NavItem = { to: string; label: string; icon: NavIconName };

const adminNav: NavItem[] = [
  { to: "/admin", label: "总览", icon: "overview" },
  { to: "/admin/logs", label: "查询日志", icon: "logs" },
  { to: "/admin/users", label: "用户", icon: "users" },
  { to: "/admin/lists", label: "公共列表", icon: "list" },
  { to: "/admin/runtime", label: "运行配置", icon: "system" },
  { to: "/admin/audit", label: "审计", icon: "audit" },
  { to: "/admin/system", label: "系统", icon: "system" },
];
const userNav: NavItem[] = [
  { to: "/app", label: "首页", icon: "overview" },
  { to: "/app/privacy", label: "安全与隐私保护", icon: "shield" },
  { to: "/app/lists", label: "订阅的公共列表", icon: "list" },
  { to: "/app/rules", label: "自定义规则", icon: "rules" },
  { to: "/app/usage", label: "统计与日志", icon: "usage" },
  { to: "/app/labs", label: "实验性功能", icon: "lab" },
  { to: "/app/advanced", label: "高级设置", icon: "system" },
  { to: "/app/lookup", label: "Lookup", icon: "lookup" },
  { to: "/app/help", label: "获取支持", icon: "help" },
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
    logs: (
      <>
        <path d="M4 5h16M4 12h16M4 19h10" />
        <circle cx="18" cy="19" r="2" />
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
    shield: (
      <>
        <path d="M12 3 20 6v5c0 5-3.4 8.3-8 10-4.6-1.7-8-5-8-10V6l8-3Z" />
        <path d="m9 12 2 2 4-4" />
      </>
    ),
    rules: (
      <>
        <path d="M5 4h14M5 10h14M5 16h9M5 22h9" />
        <circle cx="17" cy="19" r="3" />
      </>
    ),
    lookup: (
      <>
        <circle cx="11" cy="11" r="6" />
        <path d="m16 16 4 4M8.5 11h5M11 8.5v5" />
      </>
    ),
    account: (
      <>
        <circle cx="12" cy="8" r="4" />
        <path d="M4 21a8 8 0 0 1 16 0" />
      </>
    ),
    help: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M9.6 9a2.6 2.6 0 1 1 4.58 1.7c-.78.86-1.93 1.24-1.93 2.8M12 17h.01" />
      </>
    ),
    list: (
      <>
        <path d="M7 6h13M7 12h13M7 18h13" />
        <circle cx="4" cy="6" r="1" />
        <circle cx="4" cy="12" r="1" />
        <circle cx="4" cy="18" r="1" />
      </>
    ),
    lab: (
      <>
        <path d="M9 3h6M10 3v6l-5.5 9a2 2 0 0 0 1.72 3h11.56a2 2 0 0 0 1.72-3L14 9V3" />
        <path d="M8 16h8" />
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
  const location = useLocation();
  const navRef = useRef<HTMLElement>(null);
  const unsavedGuard = useRef<UnsavedGuard | undefined>(undefined);
  const registerUnsavedGuard = useCallback<RegisterUnsavedGuard>((guard) => {
    unsavedGuard.current = guard;
    return () => {
      if (unsavedGuard.current === guard) unsavedGuard.current = undefined;
    };
  }, []);
  const [logoutError, setLogoutError] = useState("");
  const admin = session?.user.role === "admin";
  const links = admin
    ? [
        ...adminNav,
        { to: "/admin/password", label: "密码", icon: "password" as const },
      ]
    : userNav;
  useEffect(() => {
    document.documentElement.scrollTop = 0;
    document.body.scrollTop = 0;
    if (window.matchMedia?.("(max-width: 840px)").matches) {
      navRef.current
        ?.querySelector<HTMLAnchorElement>("a.active")
        ?.scrollIntoView({
          behavior: window.matchMedia("(prefers-reduced-motion: reduce)")
            .matches
            ? "auto"
            : "smooth",
          block: "nearest",
          inline: "center",
        });
    }
  }, [location.pathname]);
  async function exit() {
    if (unsavedGuard.current && !unsavedGuard.current()) return;
    setLogoutError("");
    try {
      await logout();
      navigate("/login");
    } catch (e) {
      setLogoutError(message(e));
    }
  }
  return (
    <div className={admin ? "shell admin-shell" : "shell user-shell"}>
      <a className="skip-link" href="#main-content">
        跳到主要内容
      </a>
      <aside>
        <NavLink className="brand" to={admin ? "/admin" : "/app"}>
          <span className="brandmark">M</span>
          <span className="brand-copy">
            <strong>MosDNS</strong>
            <small>Control Center</small>
          </span>
        </NavLink>
        <span className="nav-caption">{admin ? "管理控制台" : "用户中心"}</span>
        <nav ref={navRef} aria-label="主导航">
          {links.map(({ to, label, icon }, i) => (
            <NavLink key={to} to={to} end={i === 0}>
              <i>
                <NavIcon name={icon} />
              </i>
              <span>{label}</span>
            </NavLink>
          ))}
        </nav>
        {admin ? (
          <div className="account">
            <span className="account-avatar" aria-hidden>
              {session?.user.username.slice(0, 1).toUpperCase()}
            </span>
            <span className="account-copy">
              <strong>{session?.user.username}</strong>
              <small>管理员</small>
            </span>
            <button className="link" onClick={exit}>
              退出登录
            </button>
          </div>
        ) : null}
      </aside>
      <main id="main-content" tabIndex={-1}>
        {!admin ? (
          <header className="topbar">
            <NavLink className="topbar-user" to="/app/account">
              {session?.user.username}
            </NavLink>
            <button className="link" onClick={exit}>
              退出登录
            </button>
          </header>
        ) : null}
        {logoutError ? <Alert error={logoutError} /> : null}
        <UnsavedGuardContext.Provider value={registerUnsavedGuard}>
          <Outlet />
        </UnsavedGuardContext.Provider>
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
  className = "",
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  className?: string;
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
      className={`modal ${className}`.trim()}
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
