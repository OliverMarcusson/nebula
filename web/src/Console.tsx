import { useEffect, useMemo, useRef, useState } from "react";
import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import { ArrowsClockwiseIcon, ChatsCircleIcon, CheckIcon, CopyIcon, DownloadSimpleIcon, MagnifyingGlassIcon, SignOutIcon, UsersIcon } from "@phosphor-icons/react";
import { fade, fadeUp, stagger } from "./motion";
import type { Dashboard } from "./hooks";
import { useCopy, useTranscript } from "./hooks";
import { downloadTranscript, fmt, restoreCommand, sessionTitle, type Session } from "./model";
import Prose from "./Prose";
import Accounts from "./Accounts";

// Icon rail, session index and a wide reading column, with keyboard
// navigation, plus the Claude accounts view.

type Filter = { kind: "all" } | { kind: "device"; id: string } | { kind: "project"; id: string };

export default function Console(d: Dashboard) {
  const [view, setView] = useState<"sessions" | "accounts">(() => (location.hash === "#accounts" ? "accounts" : "sessions"));
  const [filter, setFilter] = useState<Filter>({ kind: "all" });
  useEffect(() => {
    history.replaceState(null, "", view === "accounts" ? "#accounts" : location.pathname);
  }, [view]);
  const detected = d.accounts.filter((a) => a.state === "detected").length;
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
    filter.kind === "all" ? "Sessions" : filter.kind === "device" ? `Device ${fmt.short(filter.id)}` : projects.find(([p]) => p === filter.id)?.[1].name;
  const sync = d.error ? "Offline" : d.refreshedAt ? `Synced ${fmt.ago(new Date(d.refreshedAt).toISOString())}` : "Loading";

  return (
    <div className="relative flex h-dvh overflow-hidden bg-[#0c0a1d] font-geist text-[13px] text-[#e9e5fa]">
      <div aria-hidden className="glow" />

      <LayoutGroup id="rail">
        <nav className="relative flex w-16 shrink-0 flex-col items-center gap-2 border-r border-white/[0.06] py-4">
          <img src="/icon.png" alt="Nebula" className="mb-4 size-9" />
          <Rail label="Sessions" on={view === "sessions"} onClick={() => setView("sessions")} icon={<ChatsCircleIcon className="size-5" />} />
          <Rail
            label={detected ? `Claude accounts, ${detected} new` : "Claude accounts"}
            on={view === "accounts"}
            onClick={() => setView("accounts")}
            icon={<UsersIcon className="size-5" />}
            fresh={detected > 0}
          />
          <button
            onClick={d.refresh}
            aria-label={sync}
            title={sync}
            className={`mt-auto grid size-10 place-items-center rounded-xl transition-colors hover:bg-white/[0.05] ${d.error ? "text-[#ff9fbf]" : "text-[#8b84ad] hover:text-white"}`}
          >
            <motion.span animate={d.loading ? { rotate: 360 } : { rotate: 0 }} transition={d.loading ? { duration: 0.9, repeat: Infinity, ease: "linear" } : { duration: 0 }}>
              <ArrowsClockwiseIcon className="size-5" />
            </motion.span>
          </button>
          <button onClick={d.signOut} aria-label="Sign out" title={`Sign out ${d.owner}`} className="grid size-10 place-items-center rounded-xl text-[#8b84ad] transition-colors hover:bg-white/[0.05] hover:text-white">
            <SignOutIcon className="size-5" />
          </button>
        </nav>
      </LayoutGroup>

      <AnimatePresence mode="wait" initial={false}>
        {view === "accounts" ? (
          <motion.main key="accounts" {...fadeUp} className="relative min-w-0 flex-1">
            <Accounts {...d} />
          </motion.main>
        ) : (
          <motion.div key="sessions" {...fadeUp} className="relative flex min-w-0 flex-1">
            <section className="flex w-[330px] shrink-0 flex-col border-r border-white/[0.06]">
              <div className="px-5 pt-5">
                <div className="flex items-baseline justify-between gap-3">
                  <h1 className="truncate text-lg font-semibold tracking-tight">{heading}</h1>
                  <span className="tabular text-xs text-[#8b84ad]">{list.length}</span>
                </div>
                <label className="relative mt-4 block">
                  <span className="sr-only">Search sessions</span>
                  <MagnifyingGlassIcon className="absolute top-1/2 left-0 size-4 -translate-y-1/2 text-[#8b84ad]" />
                  <input
                    ref={search}
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    placeholder="Search"
                    className="w-full border-b border-white/10 bg-transparent py-2 pr-6 pl-6 outline-none placeholder:text-[#8b84ad] focus:border-[#a98bff]"
                  />
                  <kbd className="absolute top-1/2 right-0 -translate-y-1/2 rounded border border-white/10 px-1.5 font-geist-mono text-[10px] text-[#8b84ad]">/</kbd>
                </label>
                <select
                  value={filter.kind === "all" ? "" : `${filter.kind}:${filter.id}`}
                  onChange={(e) => {
                    const [kind, id] = e.target.value.split(":");
                    setFilter(kind ? { kind: kind as "device" | "project", id } : { kind: "all" });
                  }}
                  aria-label="Filter sessions"
                  className="mt-3 w-full rounded-lg border border-white/10 bg-[#141128] px-2.5 py-1.5 text-xs text-[#c9c2e6] outline-none focus:border-[#a98bff]"
                >
                  <option value="">All projects and devices ({d.sessions.length})</option>
                  <optgroup label="Projects">
                    {projects.map(([p, v]) => (
                      <option key={p} value={`project:${p}`}>{v.name} ({v.count})</option>
                    ))}
                  </optgroup>
                  <optgroup label="Devices">
                    {d.devices.map((v) => (
                      <option key={v.id} value={`device:${v.id}`}>{fmt.short(v.id)} ({v.sessions})</option>
                    ))}
                  </optgroup>
                </select>
              </div>
              <LayoutGroup id="sessions">
                <ul className="scroll-thin mt-3 flex-1 overflow-y-auto px-2 pb-4">
                  {d.error && <li className="px-3 py-10 text-center text-[#ff9fbf]">{d.error}</li>}
                  {!d.error && d.loading && !d.sessions.length &&
                    Array.from({ length: 6 }, (_, i) => (
                      <li key={i} className="space-y-2 py-3 pr-3 pl-4" aria-hidden>
                        <div className="h-3 w-4/5 animate-pulse rounded bg-white/[0.06]" />
                        <div className="h-2.5 w-1/3 animate-pulse rounded bg-white/[0.04]" />
                      </li>
                    ))}
                  {!d.error && !d.loading && !list.length && (
                    <li className="px-3 py-10 text-center leading-relaxed text-[#8b84ad]">
                      {d.sessions.length ? "No sessions match." : <>No sessions yet. Run <code className="font-geist-mono text-[#c9c2e6]">nebula sync</code> on a device.</>}
                    </li>
                  )}
                  <AnimatePresence initial={false} mode="popLayout">
                    {list.map((s, i) => {
                      const on = current?.key === s.key;
                      return (
                        <motion.li key={s.key} id={`row-${s.key}`} layout="position" {...stagger(i)} exit={{ opacity: 0, scale: 0.98, transition: { duration: 0.12 } }}>
                          <button
                            onClick={() => setSelected(s.key)}
                            className={`relative w-full rounded-lg py-2.5 pr-3 pl-4 text-left transition-colors ${on ? "bg-white/[0.05]" : "hover:bg-white/[0.03]"}`}
                          >
                            {on && <motion.span layoutId="session-active" className="edge absolute top-2.5 bottom-2.5 left-0 w-[3px] rounded-full" />}
                            <span className={`line-clamp-2 leading-snug ${on ? "text-white" : "text-[#c9c2e6]"}`}>{sessionTitle(s)}</span>
                            <span className="tabular mt-1 block truncate text-xs text-[#8b84ad]">
                              {s.projectName}, {fmt.ago(s.latest.stored_at)}
                              {s.revisions.length > 1 && `, ${s.revisions.length} revisions`}
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
                  <motion.div key="none" {...fade} className="grid h-full place-items-center text-[#8b84ad]">
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

function Rail({ label, on, onClick, icon, fresh }: { label: string; on: boolean; onClick: () => void; icon: React.ReactNode; fresh?: boolean }) {
  return (
    <button onClick={onClick} aria-label={label} title={label} className={`relative grid size-10 place-items-center rounded-xl transition-colors ${on ? "text-white" : "text-[#8b84ad] hover:text-white"}`}>
      {on && <motion.span layoutId="rail-active" className="absolute inset-0 rounded-xl bg-[#a98bff]/[0.16] ring-1 ring-[#a98bff]/30" />}
      <span className="relative">{icon}</span>
      <AnimatePresence>
        {fresh && <motion.span key="fresh" initial={{ scale: 0 }} animate={{ scale: 1 }} exit={{ scale: 0 }} className="absolute top-2 right-2 size-2 rounded-full bg-[#ffc9dc]" />}
      </AnimatePresence>
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
  const meta = [
    ["Model", t?.model],
    ["Branch", t?.branch],
    ["Duration", fmt.duration(t?.started, t?.ended)],
    ["Cost", t?.costUSD !== undefined ? `$${t.costUSD.toFixed(2)}` : ""],
    ["Device", fmt.short(session.deviceId)],
  ].filter(([, v]) => v);
  const counts = { transcript: t ? t.prompts + t.replies : "", revisions: session.revisions.length, files: files.length || "" };

  return (
    <div className="scroll-thin h-full overflow-y-auto">
      <div className="mx-auto max-w-[760px] px-10 pt-12 pb-24">
        <p className="flex gap-1.5 text-xs text-[#8b84ad]">
          <span className="truncate">{t?.cwd ?? session.projectPath}</span>
          <span>/</span>
          <span className="font-geist-mono">{fmt.short(session.sessionId)}</span>
        </p>
        <h2 title={sessionTitle(session, t)} className="mt-3 line-clamp-3 text-[26px] leading-[1.2] font-semibold tracking-tight text-white">{sessionTitle(session, t)}</h2>
        <dl className="mt-5 flex flex-wrap gap-x-10 gap-y-3 text-xs">
          {meta.map(([k, v]) => (
            <div key={k} className="min-w-0">
              <dt className="text-[#8b84ad]">{k}</dt>
              <dd className={`mt-0.5 truncate text-[#e9e5fa] ${k === "Branch" || k === "Device" ? "font-geist-mono" : ""}`}>{v}</dd>
            </div>
          ))}
        </dl>
        <div className="mt-6 flex items-center gap-1 border-b border-white/[0.07] pb-3">
          <LayoutGroup id="tabs">
            {(["transcript", "revisions", "files"] as const).map((k) => (
              <button key={k} onClick={() => setTab(k)} className={`relative rounded-lg px-3 py-1.5 capitalize transition-colors ${tab === k ? "text-white" : "text-[#8b84ad] hover:text-white"}`}>
                {tab === k && <motion.span layoutId="tab-active" className="absolute inset-0 rounded-lg bg-white/[0.07]" />}
                <span className="relative">
                  {k}
                  <span className="tabular ml-1.5 text-[11px] text-[#8b84ad]">{counts[k]}</span>
                </span>
              </button>
            ))}
          </LayoutGroup>
          <motion.button
            whileTap={{ scale: 0.95 }}
            onClick={() => copy(cmd)}
            aria-label="Copy restore command"
            title={copied === cmd ? "Copied" : `Copy: ${cmd}`}
            className="ml-auto grid size-8 place-items-center rounded-lg text-[#c9c2e6] hover:bg-white/[0.06] hover:text-white"
          >
            <AnimatePresence mode="wait" initial={false}>
              <motion.span key={copied === cmd ? "copied" : "copy"} initial={{ opacity: 0, scale: 0.7 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: 0.7 }} transition={{ duration: 0.12 }}>
                {copied === cmd ? <CheckIcon className="size-4 text-[#8fe3bb]" /> : <CopyIcon className="size-4" />}
              </motion.span>
            </AnimatePresence>
          </motion.button>
          <motion.button
            whileTap={{ scale: 0.97 }}
            disabled={!data}
            onClick={() => data && downloadTranscript(data.record.bundle.files, session.sessionId)}
            className="flex items-center gap-1.5 rounded-lg bg-[#e9e3ff] px-3 py-1.5 text-xs font-medium text-[#1a1240] hover:bg-white disabled:opacity-40"
          >
            <DownloadSimpleIcon className="size-3.5" />
            Download
          </motion.button>
        </div>

        <AnimatePresence mode="wait" initial={false}>
          <motion.div key={`${tab}-${revision}`} {...fade}>
            {loading && (
              <div className="mt-10 space-y-3" aria-label="Loading transcript">
                {[90, 75, 82, 40].map((w, i) => (
                  <div key={i} className="h-3.5 animate-pulse rounded bg-white/[0.06]" style={{ width: `${w}%` }} />
                ))}
              </div>
            )}
            {error && <p className="mt-10 text-[#ff9fbf]">{error}</p>}

            {tab === "transcript" && t && (
              <ol className="mt-10 space-y-5">
                {!t.entries.length && <li className="text-[#8b84ad]">No conversation records in this revision.</li>}
                {t.entries.map((e, i) =>
                  e.kind === "tool" ? (
                    <motion.li key={i} {...stagger(i)} className="truncate font-geist-mono text-xs text-[#8b84ad]">
                      <span className="text-[#c9b8ff]">{e.name}</span> {e.summary}
                    </motion.li>
                  ) : e.kind === "prompt" ? (
                    <motion.li key={i} {...stagger(i)} className="relative pt-3 pl-6">
                      <span className="edge absolute top-3 bottom-1 left-0 w-[3px] rounded-full" />
                      <div className="mb-1.5 text-xs text-[#8b84ad]">You{e.time && `, ${fmt.time(e.time)}`}</div>
                      <Prose
                        text={e.text}
                        className="space-y-3 text-[18px] leading-[1.55] font-medium tracking-tight text-white"
                        code="scroll-thin overflow-x-auto rounded-lg bg-[#141128] p-4 font-geist-mono text-xs font-normal leading-relaxed tracking-normal"
                        inline="rounded bg-white/[0.08] px-1 font-geist-mono text-[0.85em]"
                      />
                    </motion.li>
                  ) : (
                    <motion.li key={i} {...stagger(i)}>
                      <Prose
                        text={e.text}
                        className="space-y-3.5 text-[15px] leading-7 text-[#cfc9e6]"
                        code="scroll-thin overflow-x-auto rounded-lg border border-white/[0.06] bg-[#141128] p-4 font-geist-mono text-xs leading-relaxed text-[#e9e5fa]"
                        inline="rounded bg-white/[0.07] px-1.5 py-px font-geist-mono text-[0.85em] text-[#e2dbff]"
                      />
                    </motion.li>
                  ),
                )}
              </ol>
            )}

            {tab === "revisions" && (
              <ol className="mt-6">
                {session.revisions.map((r, i) => {
                  const viewing = r.revision === revision;
                  return (
                    <li key={r.revision} className="flex items-center gap-4 py-2.5">
                      <span className={`size-2 shrink-0 rounded-full ${viewing ? "edge" : "bg-white/20"}`} />
                      <span className="font-geist-mono text-xs">{r.revision.slice(0, 12)}</span>
                      {i === 0 && <span className="rounded-md border border-white/10 px-1.5 py-px text-[10px] text-[#c9c2e6]">Latest</span>}
                      <span className="ml-auto text-xs text-[#8b84ad]" title={new Date(r.stored_at).toLocaleString()}>{fmt.ago(r.stored_at)}</span>
                      <span className="tabular w-16 text-right text-xs text-[#c9c2e6]">{fmt.bytes(r.bytes)}</span>
                      <span className="tabular w-14 text-right text-xs text-[#8b84ad]">{r.file_count} {r.file_count === 1 ? "file" : "files"}</span>
                      {viewing ? (
                        <span className="w-14 text-right text-xs text-[#c9b8ff]">Viewing</span>
                      ) : (
                        <button onClick={() => { setRevision(r.revision); setTab("transcript"); }} className="w-14 text-right text-xs text-[#c9c2e6] hover:text-white">
                          View
                        </button>
                      )}
                    </li>
                  );
                })}
              </ol>
            )}

            {tab === "files" && (
              <ul className="mt-6 space-y-2.5 font-geist-mono text-xs">
                {files.map(([name, size]) => (
                  <li key={name} className="flex justify-between gap-6">
                    <span className="truncate text-[#e9e5fa]">{name}</span>
                    <span className="tabular text-[#8b84ad]">{fmt.bytes(size)}</span>
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
