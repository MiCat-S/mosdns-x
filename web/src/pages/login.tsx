import { useState, type FormEvent } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { message } from "../api";
import { Alert, Field, LanguageSelect, Spinner } from "../components";
import { useSession } from "../session";
import { t } from "../i18n";

export function Login() {
  const { session, loading, error: sessionError, retry, login } = useSession();
  const navigate = useNavigate();
  const location = useLocation();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  if (loading) return <Spinner />;
  if (sessionError)
    return (
      <div className="login">
        <section>
          <h1>{t("无法连接服务")}</h1>
          <Alert error={sessionError} />
          <button className="primary" onClick={retry}>
            {t("重试")}
          </button>
        </section>
      </div>
    );
  if (session)
    return (
      <Navigate
        to={session.user.role === "admin" ? "/admin" : "/app"}
        replace
      />
    );
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const f = new FormData(e.currentTarget);
    try {
      const s = await login(
        String(f.get("username")),
        String(f.get("password")),
      );
      const requested = (
        location.state as { from?: { pathname?: string } } | null
      )?.from?.pathname;
      navigate(
        requested &&
          requested.startsWith(s.user.role === "admin" ? "/admin" : "/app")
          ? requested
          : s.user.role === "admin"
            ? "/admin"
            : "/app",
        { replace: true },
      );
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="login">
      <div className="login-shell">
        <section>
          <div className="login-app-icon" aria-hidden>
            M
          </div>
          <p className="login-kicker" translate="no">
            MOSDNS X
          </p>
          <h1>{t("登录网络控制台")}</h1>
          <p>{t("管理 DNS 服务、访问策略与运行状态。")}</p>
          <Alert error={error} />
          <form onSubmit={submit}>
            <Field label={t("用户名")}>
              <input
                name="username"
                autoComplete="username"
                required
                placeholder={t("请输入用户名…")}
              />
            </Field>
            <Field label={t("密码")}>
              <input
                name="password"
                type="password"
                autoComplete="current-password"
                required
                placeholder={t("请输入密码…")}
              />
            </Field>
            <button className="primary wide login-submit" disabled={busy}>
              {busy ? t("正在登录…") : t("登录")}
            </button>
          </form>
          <p className="login-footnote">{t("凭证仅用于当前控制台会话")}</p>
          <LanguageSelect className="login-language" />
        </section>
      </div>
    </div>
  );
}
