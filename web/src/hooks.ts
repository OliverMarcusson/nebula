import { useCallback, useEffect, useRef, useState } from "react";
import { api, AuthError, token, type Account, type SessionRecord } from "./api";
import { devicesOf, groupSessions, parseTranscript, type Device, type Session, type Transcript } from "./model";

export type Dashboard = {
  owner: string;
  sessions: Session[];
  devices: Device[];
  accounts: Account[];
  // Runs an account mutation and adopts the server's resulting list.
  mutateAccounts: (op: () => Promise<Account[]>) => Promise<void>;
  loading: boolean;
  error: string;
  refreshedAt: number;
  refresh: () => void;
  signOut: () => void;
};

export function useDashboard(owner: string, onAuthLost: () => void): Dashboard {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [refreshedAt, setRefreshedAt] = useState(0);
  const refreshing = useRef(false);

  const refresh = useCallback(() => {
    if (refreshing.current) return;
    refreshing.current = true;
    setLoading(true);
    // Update accounts even if loading the archive fails or takes longer.
    Promise.allSettled([api.sessions().then((list) => setSessions(groupSessions(list))), api.accounts().then(setAccounts)])
      .then((results) => {
        const failures = results.filter((r) => r.status === "rejected");
        if (failures.length) throw failures.find((r) => r.reason instanceof AuthError)?.reason ?? failures[0].reason;
        setError("");
        setRefreshedAt(Date.now());
      })
      .catch((e) => {
        if (e instanceof AuthError) onAuthLost();
        else setError(e.message);
      })
      .finally(() => {
        refreshing.current = false;
        setLoading(false);
      });
  }, [onAuthLost]);

  useEffect(() => {
    refresh();
    const id = setInterval(refresh, 15_000);
    const resume = () => {
      if (!document.hidden) refresh();
    };
    document.addEventListener("visibilitychange", resume);
    window.addEventListener("focus", resume);
    window.addEventListener("online", refresh);
    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", resume);
      window.removeEventListener("focus", resume);
      window.removeEventListener("online", refresh);
    };
  }, [refresh]);

  const mutateAccounts = useCallback(
    async (op: () => Promise<Account[]>) => {
      try {
        setAccounts(await op());
        setError("");
      } catch (e) {
        if (e instanceof AuthError) onAuthLost();
        else setError(e instanceof Error ? e.message : String(e));
      }
    },
    [onAuthLost],
  );

  const signOut = useCallback(() => {
    token.clear();
    void api.logout().then(onAuthLost);
  }, [onAuthLost]);

  return { owner, sessions, devices: devicesOf(sessions), accounts, mutateAccounts, loading, error, refreshedAt, refresh, signOut };
}

export type Loaded = { record: SessionRecord; transcript: Transcript };
const cache = new Map<string, Loaded>();

// Loads one revision (default: newest) of a session; revisions are immutable so they are cached.
export function useTranscript(session: Session | undefined, revision?: string) {
  const rev = revision ?? session?.latest.revision;
  const key = session && rev ? `${session.key}/${rev}` : "";
  const [state, setState] = useState<{ key: string; data?: Loaded; error?: string }>({ key: "" });

  useEffect(() => {
    if (!session || !rev || cache.has(key)) return;
    let live = true;
    api
      .session(session.deviceId, session.sessionId, rev)
      .then((record) => {
        const data = { record, transcript: parseTranscript(record.bundle.files, session.sessionId) };
        cache.set(key, data);
        if (live) setState({ key, data });
      })
      .catch((e) => live && setState({ key, error: e.message }));
    return () => {
      live = false;
    };
  }, [key, session, rev]);

  const data = cache.get(key) ?? (state.key === key ? state.data : undefined);
  return { data, error: state.key === key ? state.error : undefined, loading: !!key && !data && state.key !== key };
}

export function useCopy() {
  const [copied, setCopied] = useState("");
  const copy = (text: string) => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(text);
      setTimeout(() => setCopied(""), 1500);
    });
  };
  return { copied, copy };
}
