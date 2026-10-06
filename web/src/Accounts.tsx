import { useEffect, useState } from "react";
import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import type { Account, Device, Limit, SignIn } from "./api";
import { api } from "./api";
import type { Dashboard } from "./hooks";
import { fmt } from "./model";
import { ease, stagger } from "./motion";

// Claude accounts that the user's devices are signed into. Connecting and
// ordering are stored on the server; switching and resets are not built yet,
// so they are shown as unavailable rather than as working controls.

const STALE_MS = 30 * 60_000;

const plans: Record<string, string> = {
  claude_pro: "Pro",
  claude_max: "Max",
  claude_team: "Team",
  claude_enterprise: "Enterprise",
};
const plan = (a: Account) => (a.organization_type ? (plans[a.organization_type] ?? a.organization_type.replace(/^claude_/, "")) : undefined);

function limitLabel(l: Limit) {
  if (l.kind === "session") return "Session";
  if (l.kind === "weekly_all") return "Weekly";
  const rest = l.kind.replace(/^weekly_/, "");
  return l.kind.startsWith("weekly_") ? `Weekly · ${rest[0].toUpperCase()}${rest.slice(1)}` : l.kind.replace(/_/g, " ");
}

function until(iso?: string) {
  if (!iso) return "";
  const m = Math.round((Date.parse(iso) - Date.now()) / 60000);
  if (m <= 0) return "resetting";
  if (m < 60) return `resets in ${m}m`;
  if (m < 48 * 60) return `resets in ${Math.floor(m / 60)}h ${m % 60}m`;
  return `resets ${new Date(iso).toLocaleDateString(undefined, { weekday: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23" })}`;
}

export default function Accounts(d: Dashboard) {
  const [adding, setAdding] = useState(false);
  const connected = d.accounts.filter((a) => a.state === "connected");
  const detected = d.accounts.filter((a) => a.state === "detected");

  const move = (i: number, by: number) => {
    const ids = connected.map((a) => a.id);
    [ids[i], ids[i + by]] = [ids[i + by], ids[i]];
    d.mutateAccounts(() => api.reorder(ids));
  };

  return (
    <div className="scroll-thin h-full overflow-y-auto">
      <LayoutGroup id="accounts">
      <div className="mx-auto max-w-[920px] px-10 pt-10 pb-24">
        <header className="flex items-start justify-between gap-6">
          <h1 className="text-xl font-medium tracking-tight">Claude accounts</h1>
          <motion.button layout whileTap={{ scale: 0.96 }} onClick={() => setAdding((v) => !v)} className="shrink-0 rounded-md bg-[#7c5cff] px-3 py-1.5 text-xs font-medium text-white hover:bg-[#8a6dff]">
            {adding ? "Close" : "Add account"}
          </motion.button>
        </header>

        <motion.div layout="position" className="mt-6 grid grid-cols-3 gap-px overflow-hidden rounded-lg border border-white/[0.06] bg-white/[0.06] text-xs">
          <Capability on title="Usage" />
          <Capability on title="Automatic switching" />
          <Capability title="Usage resets" />
        </motion.div>

        <AnimatePresence initial={false}>
          {adding && (
            <motion.div
              key="add"
              initial={{ height: 0, opacity: 0 }}
              animate={{ height: "auto", opacity: 1 }}
              exit={{ height: 0, opacity: 0 }}
              transition={{ duration: 0.28, ease }}
              className="overflow-hidden"
            >
              <AddAccount
                onDone={() => {
                  setAdding(false);
                  d.mutateAccounts(() => api.accounts());
                }}
              />
            </motion.div>
          )}
          {d.error && (
            <motion.p key="error" initial={{ opacity: 0, y: -4 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} className="mt-6 rounded-md border border-[#ff7a90]/30 bg-[#ff7a90]/[0.06] px-4 py-2.5 text-[13px] text-[#ff9aab]">
              {d.error}
            </motion.p>
          )}
        </AnimatePresence>

        <Section title="Connected" count={connected.length}>
          {connected.length === 0 && <Empty key="empty">No connected accounts</Empty>}
          {connected.map((a, i) => (
            <AccountRow key={a.id} index={i} account={a}>
              <div className="flex items-center gap-3">
                <Toggle checked={a.enabled} onChange={(v) => d.mutateAccounts(() => api.setEnabled(a.id, v))} label="Use for automatic switching" />
                <div className="flex flex-col">
                  <IconButton disabled={i === 0} onClick={() => move(i, -1)} label="Move up" path="M4 10l4-4 4 4" />
                  <IconButton disabled={i === connected.length - 1} onClick={() => move(i, 1)} label="Move down" path="M4 6l4 4 4-4" />
                </div>
                <Disconnect onConfirm={() => d.mutateAccounts(() => api.disconnect(a.id))} />
              </div>
            </AccountRow>
          ))}
        </Section>

        <AnimatePresence initial={false}>
          {(detected.length > 0 || d.accounts.length === 0) && (
            <Section key="detected" title="Detected on your devices" count={detected.length}>
              {d.accounts.length === 0 && <Empty key="empty">No accounts detected</Empty>}
              {detected.map((a, i) => (
                <AccountRow key={a.id} index={i} account={a}>
                  <motion.button
                    whileHover={{ scale: 1.03 }}
                    whileTap={{ scale: 0.96 }}
                    onClick={() => d.mutateAccounts(() => api.connect(a.id))}
                    className="rounded-md border border-[#7c5cff]/50 bg-[#7c5cff]/10 px-3 py-1.5 text-xs font-medium text-[#d6ccff] hover:bg-[#7c5cff]/20"
                  >
                    Connect
                  </motion.button>
                </AccountRow>
              ))}
            </Section>
          )}
        </AnimatePresence>
      </div>
      </LayoutGroup>
    </div>
  );
}

function Capability({ title, on }: { title: string; on?: boolean }) {
  return (
    <div className="bg-[#100f17] px-4 py-3">
      <div className="flex items-center gap-2 font-medium text-[#d9d6e4]">
        <span className={`size-1.5 rounded-full ${on ? "bg-[#5ad19a]" : "bg-[#5f5b72]"}`} />
        {title}
        <span className={`ml-auto text-[10px] font-normal ${on ? "text-[#7fe0b2]" : "text-[#77738a]"}`}>{on ? "Live" : "Planned"}</span>
      </div>
    </div>
  );
}

function Section({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  return (
    <motion.section layout="position" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="mt-10">
      <h2 className="mb-3 flex items-baseline gap-2 text-[13px] font-medium text-[#a19db3]">
        {title}
        <AnimatePresence mode="popLayout" initial={false}>
          <motion.span key={count} initial={{ opacity: 0, y: 4 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -4 }} className="tabular text-xs text-[#5f5b72]">
            {count}
          </motion.span>
        </AnimatePresence>
      </h2>
      <div className="space-y-2">
        <AnimatePresence initial={false} mode="popLayout">
          {children}
        </AnimatePresence>
      </div>
    </motion.section>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return <motion.p layout initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="rounded-lg border border-dashed border-white/[0.08] px-5 py-6 text-center text-[13px] leading-relaxed text-[#77738a]">{children}</motion.p>;
}

function AccountRow({ account: a, index, children }: { account: Account; index: number; children: React.ReactNode }) {
  const name = a.display_name || a.email || a.id.slice(0, 8);
  const p = plan(a);
  const stale = a.usage ? Date.now() - Date.parse(a.usage.observed_at) > STALE_MS : true;
  return (
    <motion.article
      layout
      layoutId={`account-${a.id}`}
      {...stagger(index)}
      exit={{ opacity: 0, scale: 0.97, transition: { duration: 0.15 } }}
      className="rounded-lg border border-white/[0.07] bg-[#14121c] px-5 py-4"
    >
      <div className="flex items-center gap-4">
        {a.state === "connected" && <span className="tabular w-4 text-center font-geist-mono text-xs text-[#5f5b72]">{a.priority}</span>}
        <span className="grid size-9 shrink-0 place-items-center rounded-full bg-gradient-to-br from-[#3b2d78] to-[#211a40] text-sm font-medium text-[#d6ccff] ring-1 ring-white/10">
          {name[0]?.toUpperCase()}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate font-medium text-[#ecebf3]">{name}</span>
            {p && <span className="rounded bg-[#7c5cff]/15 px-1.5 py-px text-[10px] font-medium text-[#c7b9ff]">{p}</span>}
            {a.state === "connected" && !a.enabled && <span className="rounded bg-white/[0.06] px-1.5 py-px text-[10px] text-[#a19db3]">Paused</span>}
            {a.limited_until && Date.parse(a.limited_until) > Date.now() && (
              <span className="rounded bg-[#e0a43a]/15 px-1.5 py-px text-[10px] font-medium text-[#f0c070]">
                Limited until {new Date(a.limited_until).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" })}
              </span>
            )}
          </div>
          <div className="mt-0.5 truncate text-xs text-[#77738a]">
            {a.display_name && a.email ? `${a.email} · ` : ""}
            {a.sightings.length
              ? `Signed in on ${a.sightings.map((s) => `${fmt.short(s.device_id)} (${s.profile})`).join(", ")} · reported ${fmt.ago(a.sightings[0].reported_at)}`
              : "Not signed in on any device"}
          </div>
        </div>
        {children}
      </div>
      <div className="mt-4 grid grid-cols-2 gap-x-8 gap-y-3 border-t border-white/[0.05] pt-3.5 pl-[68px]">
        {a.usage?.limits.length ? (
          <>
            {a.usage.limits.map((l) => (
              <Meter key={l.kind} limit={l} stale={stale} />
            ))}
            <p className="col-span-2 text-[11px] text-[#5f5b72]">
              {stale ? "Stale · " : ""}Usage as of {fmt.ago(a.usage.observed_at)} from {fmt.short(a.usage.device_id)}
            </p>
          </>
        ) : (
          <p className="col-span-2 text-xs text-[#5f5b72]">Usage unknown</p>
        )}
      </div>
    </motion.article>
  );
}

const severity: Record<string, { bar: string; label: string }> = {
  warning: { bar: "bg-[#e0a43a]", label: "Near limit" },
  critical: { bar: "bg-[#ff6b81]", label: "At limit" },
  exhausted: { bar: "bg-[#ff6b81]", label: "At limit" },
};

function Meter({ limit: l, stale }: { limit: Limit; stale: boolean }) {
  const s = l.severity && severity[l.severity];
  const pct = Math.min(100, Math.max(0, l.percent));
  return (
    <div>
      <div className="flex items-baseline justify-between text-xs">
        <span className="text-[#d9d6e4]">
          {limitLabel(l)}
          {s && (
            <span className="ml-2 inline-flex items-center gap-1 text-[11px] text-[#a19db3]">
              <svg viewBox="0 0 16 16" className="size-3" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" aria-hidden>
                <path d="M8 2.5 14.5 13.5h-13z M8 6.5v3 M8 11.5v.01" />
              </svg>
              {s.label}
            </span>
          )}
        </span>
        <span className="tabular text-[#a19db3]">
          <span className="text-[#ecebf3]">{Math.round(l.percent)}%</span> · {until(l.resets_at)}
        </span>
      </div>
      <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-white/[0.06]" role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label={`${limitLabel(l)} usage`}>
        <motion.div
          className={`h-full rounded-full ${stale ? "bg-[#5f5b72]" : s ? s.bar : "bg-[#7c5cff]"}`}
          initial={{ width: 0 }}
          animate={{ width: `${pct}%` }}
          transition={{ duration: 0.7, ease, delay: 0.1 }}
        />
      </div>
    </div>
  );
}

function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <label className="flex cursor-pointer items-center gap-2 text-xs text-[#a19db3]" title={label}>
      <span>Auto-switch</span>
      <button
        role="switch"
        aria-checked={checked}
        aria-label={label}
        onClick={() => onChange(!checked)}
        className={`relative h-[18px] w-8 rounded-full transition-colors ${checked ? "bg-[#7c5cff]" : "bg-white/[0.12]"}`}
      >
        <motion.span className="absolute top-[2px] left-[2px] size-[14px] rounded-full bg-white shadow" animate={{ x: checked ? 14 : 0 }} />
      </button>
    </label>
  );
}

function IconButton({ path, label, onClick, disabled }: { path: string; label: string; onClick: () => void; disabled?: boolean }) {
  return (
    <button onClick={onClick} disabled={disabled} aria-label={label} title={label} className="grid h-4 w-6 place-items-center rounded text-[#a19db3] hover:bg-white/[0.06] hover:text-white disabled:opacity-25 disabled:hover:bg-transparent">
      <svg viewBox="0 0 16 16" className="size-3" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
        <path d={path} />
      </svg>
    </button>
  );
}

function Disconnect({ onConfirm }: { onConfirm: () => void }) {
  const [asking, setAsking] = useState(false);
  useEffect(() => {
    if (!asking) return;
    const t = setTimeout(() => setAsking(false), 6000);
    return () => clearTimeout(t);
  }, [asking]);
  const swap = { initial: { opacity: 0, scale: 0.94 }, animate: { opacity: 1, scale: 1 }, exit: { opacity: 0, scale: 0.94 }, transition: { duration: 0.14 } };
  return (
    <AnimatePresence mode="wait" initial={false}>
      {asking ? (
        <motion.button key="confirm" {...swap} onClick={onConfirm} className="rounded-md border border-[#ff7a90]/40 bg-[#ff7a90]/10 px-3 py-1.5 text-xs text-[#ffb3c0] hover:bg-[#ff7a90]/20" title="Removes its priority and preferences. Devices stay signed in.">
          Confirm disconnect
        </motion.button>
      ) : (
        <motion.button key="ask" {...swap} onClick={() => setAsking(true)} className="rounded-md border border-white/10 px-3 py-1.5 text-xs text-[#a19db3] hover:bg-white/5 hover:text-white">
          Disconnect
        </motion.button>
      )}
    </AnimatePresence>
  );
}

// Connects an account through Claude's own sign-in, run by the companion on
// the chosen device. The browser opens there; from elsewhere, the sign-in page
// shows a code to paste here.
function AddAccount({ onDone }: { onDone: () => void }) {
  const [devices, setDevices] = useState<Device[]>();
  const [device, setDevice] = useState<string>();
  const [signIn, setSignIn] = useState<SignIn>();
  const [code, setCode] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    const load = () =>
      api
        .devices()
        .then((list) => {
          if (!live) return;
          setDevices(list);
          setDevice((d) => (d && list.some((x) => x.id === d) ? d : list[0]?.id));
        })
        .catch(() => live && setDevices([]));
    load();
    const t = setInterval(load, 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, []);

  const active = signIn && !["completed", "failed", "cancelled", "expired"].includes(signIn.state);
  useEffect(() => {
    if (!signIn || !active) return;
    const t = setInterval(() => api.signIn(signIn.id).then(setSignIn).catch(() => {}), 1000);
    return () => clearInterval(t);
  }, [signIn?.id, active]);

  useEffect(() => {
    if (signIn?.state !== "completed") return;
    const t = setTimeout(onDone, 1400);
    return () => clearTimeout(t);
  }, [signIn?.state, onDone]);

  const run = (op: () => Promise<SignIn>) => {
    setError("");
    op().then(setSignIn).catch((e: Error) => setError(e.message));
  };
  const name = (id?: string) => {
    const d = devices?.find((x) => x.id === id);
    return d ? d.name || fmt.short(d.id) : "this device";
  };
  const swap = { initial: { opacity: 0, y: 6 }, animate: { opacity: 1, y: 0 }, exit: { opacity: 0, y: -6 }, transition: { duration: 0.18, ease } };

  let body: React.ReactNode;
  let key: string;
  if (devices === undefined) {
    key = "loading";
    body = <Spinner label="Looking for devices" />;
  } else if (!signIn || signIn.state === "cancelled") {
    key = "start";
    body = devices.length ? (
      <div className="flex flex-wrap items-center gap-2">
        {devices.length > 1 &&
          devices.map((d) => (
            <button
              key={d.id}
              onClick={() => setDevice(d.id)}
              className={`rounded-md border px-3 py-1.5 text-xs transition-colors ${device === d.id ? "border-[#7c5cff]/60 bg-[#7c5cff]/15 text-white" : "border-white/10 text-[#a19db3] hover:text-white"}`}
            >
              {d.name || fmt.short(d.id)}
            </button>
          ))}
        <motion.button
          whileTap={{ scale: 0.97 }}
          disabled={!device}
          onClick={() => device && run(() => api.startSignIn(device))}
          className="ml-auto rounded-md bg-[#7c5cff] px-4 py-2 text-[13px] font-medium text-white hover:bg-[#8a6dff] disabled:opacity-50"
        >
          Sign in with Claude{devices.length === 1 ? ` on ${name(device)}` : ""}
        </motion.button>
      </div>
    ) : (
      <div className="space-y-2 text-[13px] text-[#a19db3]">
        <p>No device online. Start the companion on the computer to sign in from:</p>
        <pre className="rounded-md border border-white/[0.06] bg-[#0a0910] px-3 py-2 font-geist-mono text-xs text-[#d9d6e4]">nebula sync --watch</pre>
      </div>
    );
  } else if (signIn.state === "pending" || signIn.state === "starting" || signIn.state === "completing") {
    key = signIn.state === "completing" ? "completing" : "starting";
    body = <Spinner label={signIn.state === "completing" ? "Connecting" : `Opening Claude sign-in on ${name(signIn.device_id)}`} />;
  } else if (signIn.state === "waiting") {
    key = "waiting";
    body = (
      <div className="space-y-4">
        <div className="flex items-center justify-between gap-4">
          <Spinner label={`Waiting for sign-in on ${name(signIn.device_id)}`} />
          {signIn.url && (
            <a href={signIn.url} target="_blank" rel="noopener noreferrer" className="shrink-0 rounded-md border border-white/10 px-3 py-1.5 text-xs text-[#d9d6e4] hover:bg-white/5">
              Open sign-in page ↗
            </a>
          )}
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (code.trim()) run(() => api.submitCode(signIn.id, code.trim()));
          }}
          className="flex gap-2"
        >
          <input
            value={code}
            onChange={(e) => setCode(e.target.value)}
            placeholder="Paste code"
            autoComplete="off"
            spellCheck={false}
            className="min-w-0 flex-1 rounded-md border border-white/10 bg-[#0a0910] px-3 py-1.5 font-geist-mono text-xs outline-none focus:border-[#7c5cff] focus:ring-2 focus:ring-[#7c5cff]/20"
          />
          <button disabled={!code.trim()} className="rounded-md bg-[#7c5cff] px-3 py-1.5 text-xs font-medium text-white hover:bg-[#8a6dff] disabled:opacity-40">
            Connect
          </button>
        </form>
      </div>
    );
  } else if (signIn.state === "completed") {
    key = "completed";
    body = (
      <div className="flex items-center gap-2.5 text-[13px] text-[#d9d6e4]">
        <motion.span initial={{ scale: 0 }} animate={{ scale: 1 }} className="grid size-5 place-items-center rounded-full bg-[#5ad19a]/20 text-[#7fe0b2]">
          ✓
        </motion.span>
        Connected
      </div>
    );
  } else {
    key = "failed";
    body = (
      <div className="flex items-center justify-between gap-4 text-[13px]">
        <span className="text-[#ff9aab]">{signIn.message || "Sign-in did not complete"}</span>
        <button
          onClick={() => {
            setCode("");
            setSignIn(undefined);
          }}
          className="shrink-0 rounded-md border border-white/10 px-3 py-1.5 text-xs text-[#d9d6e4] hover:bg-white/5"
        >
          Try again
        </button>
      </div>
    );
  }

  return (
    <section className="mt-6 rounded-lg border border-[#7c5cff]/25 bg-[#7c5cff]/[0.05] p-5">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-sm font-medium">Connect a Claude account</h2>
        {active && (
          <button onClick={() => run(() => api.cancelSignIn(signIn.id))} className="text-xs text-[#a19db3] hover:text-white">
            Cancel
          </button>
        )}
      </div>
      <AnimatePresence mode="wait" initial={false}>
        <motion.div key={key} {...swap}>
          {body}
        </motion.div>
      </AnimatePresence>
      {error && <p className="mt-3 text-xs text-[#ff9aab]">{error}</p>}
    </section>
  );
}

function Spinner({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-2.5 text-[13px] text-[#d9d6e4]">
      <motion.span
        className="size-3.5 rounded-full border-2 border-[#7c5cff]/30 border-t-[#a18bff]"
        animate={{ rotate: 360 }}
        transition={{ duration: 0.9, repeat: Infinity, ease: "linear" }}
      />
      {label}
    </div>
  );
}
