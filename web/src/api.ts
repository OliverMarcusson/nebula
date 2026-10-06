// Bearer-token client for the Nebula archive API. The token lives in
// sessionStorage so it is dropped when the tab closes.

export type Snapshot = {
  device_id: string;
  session_id: string;
  project: string;
  revision: string;
  stored_at: string;
  bytes: number;
  file_count: number;
  title?: string;
};

export type SessionRecord = {
  snapshot: Snapshot;
  bundle: {
    device_id: string;
    session_id: string;
    project: string;
    files: Record<string, string>; // base64 JSONL
  };
};

export type Limit = {
  kind: string;
  group?: string;
  percent: number;
  severity?: string;
  resets_at?: string;
  active: boolean;
};

export type Account = {
  id: string;
  account_uuid: string;
  email?: string;
  display_name?: string;
  organization_uuid?: string;
  organization_name?: string;
  organization_type?: string;
  rate_limit_tier?: string;
  state: "detected" | "connected";
  enabled: boolean;
  priority: number;
  sightings: { device_id: string; profile: string; reported_at: string }[];
  usage?: { observed_at: string; device_id: string; limits: Limit[] };
  first_seen: string;
  connected_at?: string;
  limited_until?: string;
};

export type Device = { id: string; name?: string; last_seen: string };

export type SignIn = {
  id: string;
  device_id: string;
  state: "pending" | "starting" | "waiting" | "completing" | "completed" | "failed" | "cancelled" | "expired";
  url?: string;
  message?: string;
  account_id?: string;
};

export class AuthError extends Error {}

const KEY = "nebula.token";

export const token = {
  get: () => sessionStorage.getItem(KEY) ?? "",
  set: (t: string) => sessionStorage.setItem(KEY, t.trim()),
  clear: () => sessionStorage.removeItem(KEY),
};

async function request<T>(path: string, init: { method?: string; body?: unknown; bearer?: string } = {}): Promise<T> {
  // Without a device token the dashboard's Claustra session cookie authenticates.
  const bearer = init.bearer ?? token.get();
  const res = await fetch(path, {
    method: init.method ?? "GET",
    credentials: "same-origin",
    headers: {
      ...(bearer ? { Authorization: `Bearer ${bearer}` } : {}),
      ...(init.body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
    cache: "no-store",
    redirect: "error",
  });
  if (res.status === 401) throw new AuthError("Token rejected");
  if (!res.ok) throw new Error((await res.text()).trim() || `Nebula returned HTTP ${res.status}`);
  return res.json() as Promise<T>;
}

export const api = {
  authConfig: () => fetch("/auth/config", { cache: "no-store" }).then((r) => (r.ok ? (r.json() as Promise<{ claustra: boolean }>) : { claustra: false })),
  logout: () => fetch("/auth/logout", { method: "POST", credentials: "same-origin" }).catch(() => undefined),
  me: (bearer?: string) => request<{ owner: string }>("/v1/me", { bearer }),
  sessions: () => request<Snapshot[]>("/v1/sessions"),
  session: (device: string, session: string, revision?: string) =>
    request<SessionRecord>(
      `/v1/sessions/${device}/${session}` + (revision ? `?revision=${revision}` : ""),
    ),
  accounts: () => request<Account[]>("/v1/accounts"),
  connect: (id: string) => request<Account[]>(`/v1/accounts/${id}/connect`, { method: "POST" }),
  disconnect: (id: string) => request<Account[]>(`/v1/accounts/${id}/disconnect`, { method: "POST" }),
  setEnabled: (id: string, enabled: boolean) => request<Account[]>(`/v1/accounts/${id}`, { method: "PATCH", body: { enabled } }),
  devices: () => request<Device[]>("/v1/devices"),
  startSignIn: (device_id: string) => request<SignIn>("/v1/logins", { method: "POST", body: { device_id } }),
  signIn: (id: string) => request<SignIn>(`/v1/logins/${id}`),
  submitCode: (id: string, code: string) => request<SignIn>(`/v1/logins/${id}/code`, { method: "POST", body: { code } }),
  cancelSignIn: (id: string) => request<SignIn>(`/v1/logins/${id}/cancel`, { method: "POST" }),
  reorder: (ids: string[]) => request<Account[]>("/v1/accounts/order", { method: "PUT", body: { ids } }),
};
