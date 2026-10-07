import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { Alert, Shell, Spinner } from "./components";
import { useSession } from "./session";
import {
  Login,
  AdminOverview,
  AdminLogs,
  Users,
  UserDetail,
  AuditPage,
  SystemPage,
  ServicePage,
  UsagePage,
  AccountPage,
  HelpPage,
  PasswordPage,
  RuntimeConfigPage,
} from "./pages";
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
          <h1>无法连接服务</h1>
          <Alert error={error} />
          <button className="primary" onClick={retry}>
            重试
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
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route element={<Guard role="admin" />}>
        <Route path="/admin" element={<AdminOverview />} />
        <Route path="/admin/logs" element={<AdminLogs />} />
        <Route path="/admin/users" element={<Users />} />
        <Route path="/admin/users/:id" element={<UserDetail />} />
        <Route path="/admin/lists" element={<Navigate to="/admin" replace />} />
        <Route path="/admin/runtime" element={<RuntimeConfigPage />} />
        <Route path="/admin/audit" element={<AuditPage />} />
        <Route path="/admin/system" element={<SystemPage />} />
        <Route path="/admin/password" element={<PasswordPage />} />
      </Route>
      <Route element={<Guard role="user" />}>
        <Route path="/app" element={<ServicePage />} />
        <Route path="/app/usage" element={<UsagePage />} />
        {removedUserPages.map((path) => (
          <Route
            key={path}
            path={path}
            element={<Navigate to="/app" replace />}
          />
        ))}
        <Route path="/app/account" element={<AccountPage />} />
        <Route path="/app/help" element={<HelpPage />} />
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
          <button onClick={retry}>重试</button>
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
