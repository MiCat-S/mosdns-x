import {
  createContext,
  lazy,
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useId,
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
export function Empty({
  children = "暂无数据",
  action,
}: {
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="empty">
      <svg className="empty-icon" aria-hidden viewBox="0 0 24 24">
        <path d="M3 13h5l1.5 3h5L16 13h5" />
        <path d="M5.5 6.5 3 13v5a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-5l-2.5-6.5A2 2 0 0 0 16.6 5H7.4a2 2 0 0 0-1.9 1.5Z" />
      </svg>
      <span>{children}</span>
      {action}
    </div>
  );
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
      <div className="page-heading">
        <h1>{title}</h1>
        {description ? <p>{description}</p> : null}
      </div>
      {action ? <div className="page-title-action">{action}</div> : null}
    </header>
  );
}
// A number with a short unit ("12.3 ms", "0.25 次/秒") keeps the large
// numeric size; dates and words drop to body size so they don't wrap.
const numericValue = /^[\d.,]+\s?\S{0,3}$/;
function metricSize(value: ReactNode): "num" | "text" {
  if (typeof value !== "string") return "num";
  if (numericValue.test(value)) return "num";
  return value.length > 12 || /[\u3000-\u9fff]/.test(value) ? "text" : "num";
}
export function Metric({
  label,
  value,
  hint,
  size,
}: {
  label: string;
  value: ReactNode;
  hint?: string;
  size?: "num" | "text";
}) {
  const text = (size ?? metricSize(value)) === "text";
  return (
    <article className={text ? "metric metric-text" : "metric"}>
      <span className="metric-label">{label}</span>
      <strong>{value}</strong>
      {hint ? <small className="metric-hint">{hint}</small> : null}
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
  | "account"
  | "help"
  | "runtime"
  | "more";
type NavItem = {
  to: string;
  label: string;
  icon: NavIconName;
  section: string;
  // The mobile tab bar holds five items; the rest live on the "更多" page.
  only?: "desktop" | "mobile";
};

const adminNav: NavItem[] = [
  { to: "/admin", label: "总览", icon: "overview", section: "概览" },
  { to: "/admin/logs", label: "查询日志", icon: "logs", section: "监控" },
  { to: "/admin/users", label: "用户", icon: "users", section: "服务" },
  {
    to: "/admin/runtime",
    label: "运行配置",
    icon: "runtime",
    section: "服务",
  },
  {
    to: "/admin/audit",
    label: "审计",
    icon: "audit",
    section: "管理",
    only: "desktop",
  },
  {
    to: "/admin/system",
    label: "系统",
    icon: "system",
    section: "管理",
    only: "desktop",
  },
  {
    to: "/admin/more",
    label: "更多",
    icon: "more",
    section: "管理",
    only: "mobile",
  },
];
// Pages reachable from the mobile "更多" page.
export const adminMoreLinks = [
  { to: "/admin/audit", label: "审计" },
  { to: "/admin/system", label: "系统" },
  { to: "/admin/password", label: "修改密码" },
];
const userNav: NavItem[] = [
  { to: "/app", label: "首页", icon: "overview", section: "概览" },
  {
    to: "/app/usage",
    label: "统计与日志",
    icon: "usage",
    section: "工具",
  },
  { to: "/app/help", label: "获取支持", icon: "help", section: "支持" },
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
    runtime: (
      <>
        <path d="M4 7h10M18 7h2M4 17h2M10 17h10" />
        <circle cx="16" cy="7" r="2" />
        <circle cx="8" cy="17" r="2" />
      </>
    ),
    more: (
      <>
        <circle cx="5" cy="12" r="1.5" />
        <circle cx="12" cy="12" r="1.5" />
        <circle cx="19" cy="12" r="1.5" />
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
  const links = admin ? adminNav : userNav;
  // On mobile the "更多" tab stays lit while one of its pages is open.
  const inMore = adminMoreLinks.some(({ to }) => location.pathname === to);
  // Admins change their password here; users open their account centre.
  const accountLink = admin
    ? { to: "/admin/password", label: "修改密码", icon: "password" as const }
    : { to: "/app/account", label: "账户中心", icon: "account" as const };
  const sections = links.reduce<Array<{ label: string; items: NavItem[] }>>(
    (groups, item) => {
      const current = groups[groups.length - 1];
      if (current?.label === item.section) current.items.push(item);
      else groups.push({ label: item.section, items: [item] });
      return groups;
    },
    [],
  );
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
      <aside aria-label={admin ? "管理控制台" : "用户中心"}>
        <NavLink
          className="brand"
          to={admin ? "/admin" : "/app"}
          translate="no"
        >
          <span className="brandmark">M</span>
          <span className="brand-copy">
            <strong>MosDNS X</strong>
            <small>Network Console</small>
          </span>
        </NavLink>
        <nav ref={navRef} aria-label="主导航">
          {sections.map((section) => (
            <div className="nav-group" key={section.label}>
              <span className="nav-section-label">{section.label}</span>
              {section.items.map(({ to, label, icon, only }) => (
                <NavLink
                  key={to}
                  to={to}
                  end={to === (admin ? "/admin" : "/app")}
                  className={({ isActive }) =>
                    [
                      isActive || (to === "/admin/more" && inMore)
                        ? "active"
                        : "",
                      only ? `nav-${only}-only` : "",
                    ]
                      .filter(Boolean)
                      .join(" ") || undefined
                  }
                >
                  <i>
                    <NavIcon name={icon} />
                  </i>
                  <span>{label}</span>
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
        <div className="account">
          <span className="account-avatar" aria-hidden>
            {session?.user.username.slice(0, 1).toUpperCase()}
          </span>
          <span className="account-copy">
            <strong>{session?.user.username}</strong>
            <small>{admin ? "管理员" : "用户账户"}</small>
          </span>
          <NavLink
            className={({ isActive }) =>
              isActive ? "icon-link active" : "icon-link"
            }
            to={accountLink.to}
            aria-label={accountLink.label}
            title={accountLink.label}
          >
            <NavIcon name={accountLink.icon} />
          </NavLink>
          <button className="link" onClick={exit} aria-label="退出登录">
            退出
          </button>
        </div>
      </aside>
      <main id="main-content" tabIndex={-1}>
        <header className="mobile-header">
          <NavLink
            className="mobile-brand"
            to={admin ? "/admin" : "/app"}
            translate="no"
          >
            <span className="brandmark">M</span>
            <strong>MosDNS X</strong>
          </NavLink>
          <div className="mobile-account">
            {admin ? null : (
              <NavLink
                className={({ isActive }) =>
                  isActive ? "icon-link active" : "icon-link"
                }
                to={accountLink.to}
                aria-label="移动端账户中心"
                title={accountLink.label}
              >
                <NavIcon name={accountLink.icon} />
              </NavLink>
            )}
            <button className="link" onClick={exit} aria-label="移动端退出登录">
              退出
            </button>
          </div>
        </header>
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
  const titleID = useId();
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
      aria-labelledby={titleID}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="modal-panel" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2 id={titleID}>{title}</h2>
          <button
            type="button"
            className="icon-button"
            onClick={onClose}
            aria-label="关闭"
          >
            <svg aria-hidden viewBox="0 0 24 24">
              <path d="m6 6 12 12M18 6 6 18" />
            </svg>
          </button>
        </header>
        <div className="modal-body">{children}</div>
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
