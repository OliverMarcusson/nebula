import { useEffect, useMemo, useRef, useState } from "react";
import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import { fade, fadeUp, stagger } from "./motion";
import type { Dashboard } from "./hooks";
import { useCopy, useTranscript } from "./hooks";
import { downloadTranscript, fmt, restoreCommand, sessionTitle, type Session } from "./model";
import Prose from "./Prose";
import Accounts from "./Accounts";

// Three-pane workspace (filters → sessions → transcript) with keyboard
// navigation, plus the Claude accounts view.

type Filter = { kind: "all" } | { kind: "device"; id: string } | { kind: "project"; id: string };

export default function Console(d: Dashboard) {
  const [view, setView] = useState<"sessions" | "accounts">(() => (location.hash === "#accounts" ? "accounts" : "sessions"));
  const [filter, setFilterState] = useState<Filter>({ kind: "all" });
  const setFilter = (f: Filter) => {
    setFilterState(f);
    setView("sessions");
  };
  useEffect(() => {
    history.replaceState(null, "", view === "accounts" ? "#accounts" : location.pathname);
  }, [view]);
  const connected = d.accounts.filter((a) => a.state === "connected").length;
  const detected = d.accounts.length - connected;
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<string>();
  const search = useRef<HTMLInputElement>(null);

  const projects = useMemo(() => {
    const m = new Map<string, { name: string; count: number }>();
    for (const s of d.sessions) m.set(s.project, { name: s.projectName, count: (m.get(s.project)?.count ?? 0) + 1 });
    return [...m.entries()].sort((a, b) => b[1].count - a[1].count);
  }, [d.sessions]);

  const list = useMemo(() => {
    const q = query.toLowerCase();
    return d.sessions.filter(
      (s) =>
        (filter.kind === "all" || (filter.kind === "device" ? s.deviceId : s.project) === filter.id) &&
        (!q || [sessionTitle(s), s.projectPath, s.sessionId].some((v) => v.toLowerCase().includes(q))),
    );
  }, [d.sessions, filter, query]);

  const current = list.find((s) => s.key === selected) ?? list[0];

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (view !== "sessions") return;
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLSelectElement) {
        if (e.key === "Escape") (e.target as HTMLElement).blur();
        return;
      }
      if (e.key === "/") {
        e.preventDefault();
        search.current?.focus();
      }
      if (e.key === "j" || e.key === "k" || e.key === "ArrowDown" || e.key === "ArrowUp") {
        const i = current ? list.indexOf(current) : -1;
        const next = list[Math.min(list.length - 1, Math.max(0, i + (e.key === "j" || e.key === "ArrowDown" ? 1 : -1)))];
        if (next) {
          e.preventDefault();
          setSelected(next.key);
          document.getElementById(`row-${next.key}`)?.scrollIntoView({ block: "nearest" });
        }
      }
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [list, current, view]);

  const heading =
    filter.kind === "all" ? "All sessions" : filter.kind === "device" ? `Device ${fmt.short(filter.id)}` : projects.find(([p]) => p === filter.id)?.[1].name;

  return (
    <div className="flex h-dvh overflow-hidden bg-[#0e0d14] font-geist text-[13px] text-[#e8e6f0]">
      <aside className="flex w-[232px] shrink-0 flex-col border-r border-white/[0.06] bg-[#0a0910]">
        <div className="flex items-center gap-2.5 px-4 pt-4 pb-5">
          <img src="/icon.png" alt="" className="size-7" />
          <div className="min-w-0 leading-tight">
            <div className="font-medium">Nebula</div>
            <div className="truncate text-xs text-[#77738a]">{d.owner}</div>
          </div>
        </div>
        <LayoutGroup id="nav">
        <nav className="scroll-thin flex-1 space-y-6 overflow-y-auto px-2">
          <div className="space-y-px">
            <NavItem active={view === "sessions" && filter.kind === "all"} onClick={() => setFilter({ kind: "all" })} label="All sessions" count={d.sessions.length} />
            <NavItem
              active={view === "accounts"}
              onClick={() => setView("accounts")}
              label="Claude accounts"
              count={connected}
              badge={detected > 0 ? `${detected} new` : undefined}
            />
          </div>
          <NavGroup title="Devices">
            {d.devices.map((v) => (
              <NavItem key={v.id} mono active={view === "sessions" && filter.kind === "device" && filter.id === v.id} onClick={() => setFilter({ kind: "device", id: v.id })} label={fmt.short(v.id)} count={v.sessions} />
            ))}
          </NavGroup>
          <NavGroup title="Projects">
            {projects.map(([p, v]) => (
              <NavItem key={p} active={view === "sessions" && filter.kind === "project" && filter.id === p} onClick={() => setFilter({ kind: "project", id: p })} label={v.name} count={v.count} />
            ))}
          </NavGroup>
        </nav>
        </LayoutGroup>
        <div className="flex items-center justify-between border-t border-white/[0.06] px-4 py-3 text-xs text-[#77738a]">
          <button onClick={d.refresh} className="flex items-center gap-2 hover:text-[#e8e6f0]">
            <span className={`size-1.5 rounded-full ${d.error ? "bg-[#ff7a90]" : d.loading ? "animate-pulse bg-[#a18bff]" : "bg-[#5ad19a]"}`} />
            {d.error ? "Offline" : d.refreshedAt ? `Synced ${fmt.ago(new Date(d.refreshedAt).toISOString())}` : "Loading"}
          </button>
          <button onClick={d.signOut} className="hover:text-[#e8e6f0]">Sign out</button>
        </div>
      </aside>

      <AnimatePresence mode="wait" initial={false}>
      {view === "accounts" ? (
        <motion.main key="accounts" {...fadeUp} className="min-w-0 flex-1">
          <Accounts {...d} />
        </motion.main>
      ) : (
        <motion.div key="sessions" {...fadeUp} className="flex min-w-0 flex-1">
          <section className="flex w-[380px] shrink-0 flex-col border-r border-white/[0.06]">
            <div className="border-b border-white/[0.06] px-4 pt-4 pb-3">
              <div className="flex items-baseline justify-between">
                <h1 className="truncate text-[15px] font-medium">{heading}</h1>
                <span className="tabular text-xs text-[#77738a]">{list.length}</span>
              </div>
              <div className="relative mt-3">
                <input
                  ref={search}
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Search sessions"
                  className="w-full rounded-md border border-white/[0.08] bg-[#14121c] py-1.5 pr-8 pl-3 outline-none placeholder:text-[#5f5b72] focus:border-[#7c5cff] focus:ring-2 focus:ring-[#7c5cff]/20"
                />
                <kbd className="absolute top-1/2 right-2 -translate-y-1/2 rounded border border-white/10 px-1.5 font-geist-mono text-[10px] text-[#77738a]">/</kbd>
              </div>
            </div>
            <LayoutGroup id="sessions">
            <ul className="scroll-thin flex-1 overflow-y-auto p-2">
              {d.error && <li className="p-6 text-center text-[#ff7a90]">{d.error}</li>}
              {!d.error && !d.loading && !list.length && (
                <li className="p-6 text-center text-[#77738a]">{d.sessions.length ? "No sessions match." : "No sessions yet. Run nebula sync on a device."}</li>
              )}
              <AnimatePresence initial={false} mode="popLayout">
              {list.map((s, i) => {
                const on = current?.key === s.key;
                return (
                  <motion.li
                    key={s.key}
                    id={`row-${s.key}`}
                    layout="position"
                    {...stagger(i)}
                    exit={{ opacity: 0, scale: 0.98, transition: { duration: 0.12 } }}
                  >
                    <button
                      onClick={() => setSelected(s.key)}
                      className={`relative w-full rounded-lg px-3 py-2.5 text-left transition-colors ${on ? "" : "hover:bg-white/[0.035]"}`}
                    >
                      {on && (
                        <motion.span
                          layoutId="session-active"
                          className="absolute inset-0 rounded-lg bg-[#7c5cff]/[0.14] shadow-[inset_0_0_0_1px_rgba(124,92,255,0.35)]"
                        />
                      )}
                      <span className="relative block">
                      <div className="flex items-start justify-between gap-3">
                        <span className={`line-clamp-2 leading-snug ${on ? "text-white" : "text-[#d9d6e4]"}`}>{sessionTitle(s)}</span>
                        <span className="tabular shrink-0 pt-0.5 text-[11px] text-[#77738a]">{fmt.ago(s.latest.stored_at)}</span>
                      </div>
                      <div className="mt-1 flex items-center gap-2 text-xs text-[#77738a]">
                        <span className="truncate">{s.projectName}</span>
                        {s.revisions.length > 1 && <span className="rounded bg-white/[0.06] px-1.5 py-px font-geist-mono text-[10px] text-[#a19db3]">{s.revisions.length} revs</span>}
                      </div>
                      </span>
                    </button>
                  </motion.li>
                );
              })}
              </AnimatePresence>
            </ul>
            </LayoutGroup>
          </section>

          <main className="relative min-w-0 flex-1">
            <AnimatePresence mode="wait" initial={false}>
              {current ? (
                <motion.div key={current.key} {...fade} className="h-full">
                  <Detail session={current} />
                </motion.div>
              ) : (
                <motion.div key="none" {...fade} className="grid h-full place-items-center text-[#5f5b72]">
                  Select a session
                </motion.div>
              )}
            </AnimatePresence>
          </main>
        </motion.div>
      )}
      </AnimatePresence>
    </div>
  );
}

function NavGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="px-2 pb-1.5 text-[11px] font-medium text-[#5f5b72]">{title}</div>
      <div className="space-y-px">{children}</div>
    </div>
  );
}

function NavItem({ label, count, active, onClick, mono, badge }: { label: string; count: number; active: boolean; onClick: () => void; mono?: boolean; badge?: string }) {
  return (
    <button onClick={onClick} className={`relative flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left transition-colors ${active ? "text-white" : "text-[#a19db3] hover:bg-white/[0.04] hover:text-[#e8e6f0]"}`}>
      {active && <motion.span layoutId="nav-active" className="absolute inset-0 rounded-md bg-white/[0.07]" />}
      <span className={`relative truncate ${mono ? "font-geist-mono text-xs" : ""}`}>{label}</span>
      <span className="relative flex items-center gap-1.5">
        <AnimatePresence>
          {badge && (
            <motion.span key="badge" initial={{ opacity: 0, scale: 0.8 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: 0.8 }} className="rounded bg-[#7c5cff]/20 px-1.5 py-px text-[10px] font-medium text-[#c7b9ff]">
              {badge}
            </motion.span>
          )}
        </AnimatePresence>
        <span className="tabular text-xs text-[#5f5b72]">{count}</span>
      </span>
    </button>
  );
}

function Detail({ session }: { session: Session }) {
  const [tab, setTab] = useState<"transcript" | "revisions" | "files">("transcript");
  const [revision, setRevision] = useState(session.latest.revision);
  const { data, error, loading } = useTranscript(session, revision);
  const { copied, copy } = useCopy();
  const t = data?.transcript;
  const cmd = restoreCommand(session, revision);
  const files = data ? Object.entries(data.record.bundle.files).map(([n, b]) => [n, Math.floor((b.length * 3) / 4)] as const) : [];

  return (
    <div className="flex h-full flex-col">
      <header className="border-b border-white/[0.06] px-8 pt-5">
        <div className="flex items-center gap-1.5 text-xs text-[#77738a]">
          <span className="truncate">{t?.cwd ?? session.projectPath}</span>
          <span>/</span>
          <span className="font-geist-mono">{fmt.short(session.sessionId)}</span>
        </div>
        <div className="mt-2 flex items-start justify-between gap-6">
          <h2 className="text-xl leading-snug font-medium tracking-tight">{sessionTitle(session, t)}</h2>
          <div className="flex shrink-0 gap-2">
            <button onClick={() => copy(cmd)} className="rounded-md border border-white/10 px-3 py-1.5 text-xs text-[#d9d6e4] hover:bg-white/5">
              <AnimatePresence mode="wait" initial={false}>
                <motion.span key={copied === cmd ? "copied" : "copy"} initial={{ opacity: 0, y: 4 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -4 }} transition={{ duration: 0.12 }} className="block">
                  {copied === cmd ? "Copied ✓" : "Copy restore command"}
                </motion.span>
              </AnimatePresence>
            </button>
            <button disabled={!data} onClick={() => data && downloadTranscript(data.record.bundle.files, session.sessionId)} className="rounded-md bg-[#7c5cff] px-3 py-1.5 text-xs font-medium text-white hover:bg-[#8a6dff] disabled:opacity-40">
              Download
            </button>
          </div>
        </div>
        <div className="mt-3 flex flex-wrap gap-x-5 gap-y-1 text-xs text-[#77738a]">
          {t?.model && <Meta k="Model" v={t.model} />}
          {t?.branch && <Meta k="Branch" v={t.branch} mono />}
          {fmt.duration(t?.started, t?.ended) && <Meta k="Duration" v={fmt.duration(t?.started, t?.ended)} />}
          {t?.costUSD !== undefined && <Meta k="Cost" v={`$${t.costUSD.toFixed(2)}`} />}
          <Meta k="Device" v={fmt.short(session.deviceId)} mono />
        </div>
        <nav className="mt-4 flex gap-5">
          {(["transcript", "revisions", "files"] as const).map((k) => (
            <button key={k} onClick={() => setTab(k)} className={`relative pb-2.5 capitalize transition-colors ${tab === k ? "text-white" : "text-[#77738a] hover:text-[#d9d6e4]"}`}>
              {tab === k && <motion.span layoutId="tab-underline" className="absolute inset-x-0 -bottom-px h-0.5 rounded-full bg-[#7c5cff]" />}
              {k}
              <span className="ml-1.5 text-[11px] text-[#5f5b72]">{k === "transcript" ? (t ? t.prompts + t.replies : "") : k === "revisions" ? session.revisions.length : files.length || ""}</span>
            </button>
          ))}
        </nav>
      </header>

      <div className="scroll-thin flex-1 overflow-y-auto">
        <AnimatePresence mode="wait" initial={false}>
        <motion.div key={`${tab}-${revision}`} {...fade}>
        {loading && <p className="p-8 text-[#77738a]">Loading transcript…</p>}
        {error && <p className="p-8 text-[#ff7a90]">{error}</p>}

        {tab === "transcript" && t && (
          <ol className="mx-auto max-w-[760px] space-y-5 px-8 py-8">
            {!t.entries.length && <li className="text-[#77738a]">No conversation records in this revision.</li>}
            {t.entries.map((e, i) =>
              e.kind === "tool" ? (
                <motion.li key={i} {...stagger(i)} className="-mt-3 flex items-center gap-2 pl-9 text-xs text-[#77738a]">
                  <span className="rounded border border-white/[0.07] bg-[#14121c] px-1.5 py-0.5 font-geist-mono text-[11px] text-[#a19db3]">{e.name}</span>
                  <span className="truncate">{e.summary}</span>
                </motion.li>
              ) : (
                <motion.li key={i} {...stagger(i)} className="flex gap-3">
                  <span className={`mt-0.5 grid size-6 shrink-0 place-items-center rounded-full text-[10px] font-semibold ${e.kind === "prompt" ? "bg-[#7c5cff] text-white" : "bg-white/[0.08] text-[#a19db3]"}`}>
                    {e.kind === "prompt" ? "Y" : "C"}
                  </span>
                  <div className={`min-w-0 flex-1 ${e.kind === "prompt" ? "rounded-lg border border-[#7c5cff]/25 bg-[#7c5cff]/[0.07] px-4 py-3" : "pt-0.5"}`}>
                    <div className="mb-1 flex items-baseline gap-2 text-xs">
                      <span className="font-medium text-[#d9d6e4]">{e.kind === "prompt" ? "You" : "Claude"}</span>
                      <span className="text-[#5f5b72]">{fmt.time(e.time)}</span>
                    </div>
                    <Prose
                      text={e.text}
                      className="space-y-2.5 text-[13.5px] leading-relaxed text-[#cfccdb]"
                      code="scroll-thin overflow-x-auto rounded-md border border-white/[0.06] bg-[#0a0910] p-3 font-geist-mono text-xs leading-relaxed text-[#d9d6e4]"
                      inline="rounded bg-white/[0.07] px-1 py-px font-geist-mono text-[0.88em] text-[#e2dcff]"
                    />
                  </div>
                </motion.li>
              ),
            )}
          </ol>
        )}

        {tab === "revisions" && (
          <table className="w-full text-left">
            <thead className="text-[11px] text-[#5f5b72]">
              <tr className="border-b border-white/[0.06]">
                {["Revision", "Stored", "Size", "Files", ""].map((h) => (
                  <th key={h} className="px-8 py-2.5 font-medium">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {session.revisions.map((r, i) => (
                <tr key={r.revision} className={`border-b border-white/[0.04] ${r.revision === revision ? "bg-[#7c5cff]/[0.08]" : ""}`}>
                  <td className="px-8 py-3 font-geist-mono text-xs">{r.revision.slice(0, 12)}{i === 0 && <span className="ml-2 rounded bg-[#5ad19a]/15 px-1.5 py-px font-geist text-[10px] text-[#7fe0b2]">latest</span>}</td>
                  <td className="px-8 py-3 text-[#a19db3]">{new Date(r.stored_at).toLocaleString()}</td>
                  <td className="tabular px-8 py-3 text-[#a19db3]">{fmt.bytes(r.bytes)}</td>
                  <td className="tabular px-8 py-3 text-[#a19db3]">{r.file_count}</td>
                  <td className="px-8 py-3 text-right">
                    {r.revision === revision ? (
                      <span className="text-xs text-[#a18bff]">Viewing</span>
                    ) : (
                      <button onClick={() => { setRevision(r.revision); setTab("transcript"); }} className="text-xs text-[#a19db3] hover:text-white">View</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        {tab === "files" && (
          <ul className="px-8 py-4">
            {files.map(([name, size]) => (
              <li key={name} className="flex justify-between border-b border-white/[0.04] py-2.5 font-geist-mono text-xs">
                <span className="truncate text-[#d9d6e4]">{name}</span>
                <span className="tabular text-[#77738a]">{fmt.bytes(size)}</span>
              </li>
            ))}
          </ul>
        )}
        </motion.div>
        </AnimatePresence>
      </div>
    </div>
  );
}

function Meta({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <span>
      {k} <span className={`text-[#d9d6e4] ${mono ? "font-geist-mono" : ""}`}>{v}</span>
    </span>
  );
}
