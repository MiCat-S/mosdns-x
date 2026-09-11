import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  APIError,
  clearSecrets,
  json,
  message,
  onUnauthorized,
  request,
  setCSRF,
} from "./api";
import type { Session } from "./types";
type State = {
  session: Session | null;
  loading: boolean;
  error: string;
  retry: () => void;
  login: (username: string, password: string) => Promise<Session>;
  logout: () => Promise<void>;
  clearLocal: () => void;
};
const Context = createContext<State | null>(null);
export function SessionProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const expire = useCallback(() => {
    clearSecrets();
    setSession(null);
  }, []);
  useEffect(() => onUnauthorized(expire), [expire]);
  useEffect(() => {
    const c = new AbortController();
    setLoading(true);
    setError("");
    request<Session>("/session", { signal: c.signal })
      .then((s) => {
        if (c.signal.aborted) return;
        setCSRF(s.csrf_token);
        setSession(s);
      })
      .catch((e) => {
        if (c.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) expire();
        else setError(message(e));
      })
      .finally(() => {
        if (!c.signal.aborted) setLoading(false);
      });
    return () => c.abort();
  }, [expire, attempt]);
  const login = useCallback(async (username: string, password: string) => {
    const s = await request<Session>(
      "/session",
      json("POST", { username, password }),
    );
    setCSRF(s.csrf_token);
    setSession(s);
    return s;
  }, []);
  const logout = useCallback(async () => {
    try {
      await request("/session", { method: "DELETE" });
      expire();
    } catch (e) {
      if (e instanceof APIError && e.status === 401) {
        expire();
        return;
      }
      throw e;
    }
  }, [expire]);
  return (
    <Context.Provider
      value={useMemo(
        () => ({
          session,
          loading,
          error,
          retry: () => setAttempt((x) => x + 1),
          login,
          logout,
          clearLocal: expire,
        }),
        [session, loading, error, login, logout, expire],
      )}
    >
      {children}
    </Context.Provider>
  );
}
export function useSession() {
  const v = useContext(Context);
  if (!v) throw Error("SessionProvider 缺失");
  return v;
}
