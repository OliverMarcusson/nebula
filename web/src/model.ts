import type { Snapshot } from "./api";

export type Session = {
  key: string;
  deviceId: string;
  sessionId: string;
  project: string;
  projectPath: string;
  projectName: string;
  latest: Snapshot;
  revisions: Snapshot[]; // newest first
};

export type Device = {
  id: string;
  sessions: number;
  revisions: number;
  bytes: number;
  lastSeen: string;
};

// Claude Code names project directories by replacing path separators with "-".
// The mapping is lossy (real hyphens look the same), so this is display-only.
export function decodeProject(dir: string): string {
  const win = /^([A-Za-z])--(.*)$/.exec(dir);
  if (win) return `${win[1]}:\\${win[2].replace(/-/g, "\\")}`;
  return dir.startsWith("-") ? dir.replace(/-/g, "/") : dir;
}

export function groupSessions(list: Snapshot[]): Session[] {
  const map = new Map<string, Session>();
  for (const s of list) {
    const key = `${s.device_id}/${s.session_id}`;
    let g = map.get(key);
    if (!g) {
      const path = decodeProject(s.project);
      g = {
        key,
        deviceId: s.device_id,
        sessionId: s.session_id,
        project: s.project,
        projectPath: path,
        projectName: path.split(/[\\/]/).filter(Boolean).pop() ?? path,
        latest: s,
        revisions: [],
      };
      map.set(key, g);
    }
    g.revisions.push(s);
  }
  // The API returns snapshots newest first, so revisions[0] is the latest.
  for (const g of map.values()) g.latest = g.revisions[0];
  return [...map.values()].sort((a, b) => b.latest.stored_at.localeCompare(a.latest.stored_at));
}

export function devicesOf(sessions: Session[]): Device[] {
  const map = new Map<string, Device>();
  for (const s of sessions) {
    const d = map.get(s.deviceId) ?? { id: s.deviceId, sessions: 0, revisions: 0, bytes: 0, lastSeen: "" };
    d.sessions++;
    d.revisions += s.revisions.length;
    d.bytes += s.latest.bytes;
    if (s.latest.stored_at > d.lastSeen) d.lastSeen = s.latest.stored_at;
    map.set(s.deviceId, d);
  }
  return [...map.values()].sort((a, b) => b.lastSeen.localeCompare(a.lastSeen));
}

export type Entry =
  | { kind: "prompt"; text: string; time?: string }
  | { kind: "reply"; text: string; time?: string }
  | { kind: "tool"; name: string; summary: string; time?: string };

export type Transcript = {
  title?: string;
  cwd?: string;
  branch?: string;
  model?: string;
  started?: string;
  ended?: string;
  costUSD?: number;
  entries: Entry[];
  prompts: number;
  replies: number;
  tools: number;
  subagents: string[];
};

export function decodeBase64(b64: string): string {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

type Block = { type: string; text?: string; name?: string; input?: Record<string, unknown> };

function toolSummary(input: Record<string, unknown> = {}): string {
  for (const k of ["description", "command", "file_path", "pattern", "url", "query", "prompt"]) {
    const v = input[k];
    if (typeof v === "string" && v) return v.split("\n")[0].slice(0, 200);
  }
  return "";
}

export function parseTranscript(files: Record<string, string>, sessionId: string): Transcript {
  const t: Transcript = { entries: [], prompts: 0, replies: 0, tools: 0, subagents: [] };
  t.subagents = Object.keys(files).filter((f) => f !== `${sessionId}.jsonl`).sort();
  const main = files[`${sessionId}.jsonl`];
  if (!main) return t;
  for (const line of decodeBase64(main).split("\n")) {
    if (!line.trim()) continue;
    let o: any;
    try {
      o = JSON.parse(line);
    } catch {
      continue;
    }
    const time: string | undefined = o.timestamp;
    if (time) {
      t.started ??= time;
      t.ended = time;
    }
    if (o.cwd) t.cwd ??= o.cwd;
    if (o.gitBranch) t.branch = o.gitBranch;
    if (o.type === "ai-title" && o.aiTitle) t.title = o.aiTitle;
    if (o.type === "summary" && o.summary && !t.title) t.title = o.summary;
    if (o.type === "cost-state" && typeof o.totalCostUSD === "number") t.costUSD = o.totalCostUSD;
    if (o.isMeta || o.isSidechain) continue;
    const content = o.message?.content;
    if (o.type === "user") {
      const text = typeof content === "string" ? content : (content as Block[] | undefined)?.find((b) => b.type === "text")?.text;
      // Tool results and harness-injected tags (<system-reminder>, <command-name>, ...) are not prompts.
      if (text && !text.trimStart().startsWith("<")) {
        t.entries.push({ kind: "prompt", text, time });
        t.prompts++;
      }
    } else if (o.type === "assistant" && Array.isArray(content)) {
      if (o.message?.model && o.message.model !== "<synthetic>") t.model = o.message.model;
      for (const b of content as Block[]) {
        if (b.type === "text" && b.text?.trim()) {
          t.entries.push({ kind: "reply", text: b.text, time });
          t.replies++;
        } else if (b.type === "tool_use") {
          t.entries.push({ kind: "tool", name: b.name ?? "tool", summary: toolSummary(b.input), time });
          t.tools++;
        }
      }
    }
  }
  return t;
}

export function sessionTitle(s: Session, t?: Transcript): string {
  return t?.title ?? s.latest.title ?? `Untitled · ${s.sessionId.slice(0, 8)}`;
}

export const fmt = {
  bytes(n: number): string {
    if (n < 1024) return `${n} B`;
    const units = ["KB", "MB", "GB"];
    let v = n / 1024;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
  },
  ago(iso: string, now = Date.now()): string {
    const s = Math.max(0, (now - Date.parse(iso)) / 1000);
    if (s < 60) return "just now";
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    if (s < 86400 * 30) return `${Math.floor(s / 86400)}d ago`;
    return new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
  },
  time(iso?: string): string {
    return iso ? new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" }) : "";
  },
  date(iso: string): string {
    return new Date(iso).toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
  },
  day(iso: string): string {
    const d = new Date(iso);
    const today = new Date();
    const y = new Date(today);
    y.setDate(today.getDate() - 1);
    if (d.toDateString() === today.toDateString()) return "Today";
    if (d.toDateString() === y.toDateString()) return "Yesterday";
    return d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" });
  },
  duration(a?: string, b?: string): string {
    if (!a || !b) return "";
    const m = Math.round((Date.parse(b) - Date.parse(a)) / 60000);
    if (m < 60) return `${m} min`;
    return `${Math.floor(m / 60)}h ${m % 60}m`;
  },
  short: (id: string) => id.slice(0, 8),
};

export const restoreCommand = (s: Session, revision?: string) =>
  `nebula restore --device ${s.deviceId} --session ${s.sessionId}` +
  (revision && revision !== s.latest.revision ? ` --revision ${revision}` : "");

export function downloadTranscript(files: Record<string, string>, sessionId: string) {
  const name = `${sessionId}.jsonl`;
  const blob = new Blob([decodeBase64(files[name] ?? "")], { type: "application/x-ndjson" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
