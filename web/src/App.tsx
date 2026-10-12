import {
  lazy,
  Suspense,
  type ComponentType,
  type LazyExoticComponent,
} from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { Alert, Shell, Spinner } from "./components";
import { useSession } from "./session";
import { t, useLang } from "./i18n";

// Each page module is its own chunk, so the login page doesn't download
// the admin console and users don't download admin pages.
function lazyPage<M, K extends keyof M>(load: () => Promise<M>, name: K) {
  return lazy(() =>
    load().then((m) => ({ default: m[name] as ComponentType })),
  );
}
const loadAdmin = () => import("./pages/admin");
const loadAdminUsers = () => import("./pages/admin-users");
const loadUser = () => import("./pages/user");
const Login = lazyPage(() => import("./pages/login"), "Login");
const AdminOverview = lazyPage(loadAdmin, "AdminOverview");
const AdminLogs = lazyPage(loadAdmin, "AdminLogs");
const AuditPage = lazyPage(loadAdmin, "AuditPage");
const SystemPage = lazyPage(loadAdmin, "SystemPage");
const PasswordPage = lazyPage(loadAdmin, "PasswordPage");
const MorePage = lazyPage(loadAdmin, "MorePage");
const Users = lazyPage(loadAdminUsers, "Users");
const UserDetail = lazyPage(loadAdminUsers, "UserDetail");
const RuntimeConfigPage = lazyPage(
  () => import("./runtime-config"),
  "RuntimeConfigPage",
);
const ServicePage = lazyPage(loadUser, "ServicePage");
const UsagePage = lazyPage(loadUser, "UsagePage");
const AccountPage = lazyPage(loadUser, "AccountPage");
const HelpPage = lazyPage(loadUser, "HelpPage");

// The suspense boundary sits inside each route, so the shell and its
// navigation stay on screen while a page's chunk loads.
function page(Page: LazyExoticComponent<ComponentType>) {
  return (
    <Suspense fallback={<Spinner />}>
      <Page />
    </Suspense>
  );
}
// Pages removed from the slimmed panel; old bookmarks land on the home page.
const removedUserPages = [
  "/app/privacy",
  "/app/lists",
  "/app/rules",
  "/app/labs",
  "/app/advanced",
  "/app/lookup",
];
function Guard({ role }: { role: "admin" | "user" }) {
  const { session, loading, error, retry } = useSession();
  const loc = useLocation();
  if (loading) return <Spinner />;
  if (error)
    return (
      <div className="login">
        <section>
          <h1>{t("无法连接服务")}</h1>
          <Alert error={error} />
          <button className="primary" onClick={retry}>
            {t("重试")}
          </button>
        </section>
      </div>
    );
  if (!session) return <Navigate to="/login" state={{ from: loc }} replace />;
  if (session.user.role !== role)
    return (
      <Navigate
        to={session.user.role === "admin" ? "/admin" : "/app"}
        replace
      />
    );
  return <Shell />;
}
export function App() {
  // Subscribing here renders every route again when the language changes,
  // without remounting pages; see LanguageProvider.
  useLang();
  return (
    <Routes>
      <Route path="/login" element={page(Login)} />
      <Route element={<Guard role="admin" />}>
        <Route path="/admin" element={page(AdminOverview)} />
        <Route path="/admin/logs" element={page(AdminLogs)} />
        <Route path="/admin/users" element={page(Users)} />
        <Route path="/admin/users/:id" element={page(UserDetail)} />
        <Route path="/admin/lists" element={<Navigate to="/admin" replace />} />
        <Route path="/admin/runtime" element={page(RuntimeConfigPage)} />
        <Route path="/admin/audit" element={page(AuditPage)} />
        <Route path="/admin/system" element={page(SystemPage)} />
        <Route path="/admin/password" element={page(PasswordPage)} />
        <Route path="/admin/more" element={page(MorePage)} />
      </Route>
      <Route element={<Guard role="user" />}>
        <Route path="/app" element={page(ServicePage)} />
        <Route path="/app/usage" element={page(UsagePage)} />
        {removedUserPages.map((path) => (
          <Route
            key={path}
            path={path}
            element={<Navigate to="/app" replace />}
          />
        ))}
        <Route path="/app/account" element={page(AccountPage)} />
        <Route path="/app/help" element={page(HelpPage)} />
        <Route
          path="/app/credentials"
          element={<Navigate to="/app/account" replace />}
        />
        <Route
          path="/app/password"
          element={<Navigate to="/app/account" replace />}
        />
      </Route>
      <Route path="*" element={<RootRedirect />} />
    </Routes>
  );
}
function RootRedirect() {
  const { session, loading, error, retry } = useSession();
  if (loading) return <Spinner />;
  if (error)
    return (
      <div className="login">
        <section>
          <Alert error={error} />
          <button onClick={retry}>{t("重试")}</button>
        </section>
      </div>
    );
  return (
    <Navigate
      to={
        !session ? "/login" : session.user.role === "admin" ? "/admin" : "/app"
      }
      replace
    />
  );
}
