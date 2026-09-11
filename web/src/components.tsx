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

const adminNav = [
  ["/admin", "总览", "⌁"],
  ["/admin/users", "用户", "♙"],
  ["/admin/audit", "审计", "◷"],
  ["/admin/system", "系统", "⚙"],
];
const userNav = [
  ["/app", "我的服务", "⌁"],
  ["/app/usage", "用量", "▥"],
  ["/app/credentials", "凭证", "⌘"],
  ["/app/password", "密码", "●"],
];
export function Shell() {
  const { session, logout } = useSession();
  const navigate = useNavigate();
  const [logoutError, setLogoutError] = useState("");
  const admin = session?.user.role === "admin";
  const links = admin
    ? [...adminNav, ["/admin/password", "密码", "●"]]
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
          <span>MosDNS</span>
        </NavLink>
        <nav aria-label="主导航">
          {links.map(([to, label, icon], i) => (
            <NavLink key={to} to={to} end={i === 0}>
              <i aria-hidden>{icon}</i>
              {label}
            </NavLink>
          ))}
        </nav>
        <div className="account">
          <span>{session?.user.username}</span>
          <small>{admin ? "管理员" : "用户"}</small>
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
