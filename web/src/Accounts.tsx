import { useEffect, useState } from "react";
import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import { ArrowSquareOutIcon, CaretDownIcon, CaretUpIcon, CheckIcon, PlusIcon, WarningIcon, XIcon } from "@phosphor-icons/react";
import type { Account, Grant, Limit, Reset, SignIn } from "./api";
import { api, isShared, SHARED_DEVICE } from "./api";
import type { Dashboard } from "./hooks";
import { fmt } from "./model";
import { ease, stagger } from "./motion";

// Claude accounts that the user's devices are signed into: connecting,
// ordering, usage, and the usage resets each account has to spend.

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
  return l.kind.startsWith("weekly_") ? `Weekly, ${rest[0].toUpperCase()}${rest.slice(1)}` : l.kind.replace(/_/g, " ");
}

function until(iso?: string) {
  if (!iso) return "";
  const m = Math.round((Date.parse(iso) - Date.now()) / 60000);
  if (m <= 0) return "resetting";
  if (m < 60) return `resets in ${m}m`;
  if (m < 48 * 60) return `resets in ${Math.floor(m / 60)}h ${m % 60}m`;
  return `resets ${new Date(iso).toLocaleDateString(undefined, { weekday: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23" })}`;
}

const primary = "rounded-lg bg-[#e9e3ff] font-medium text-[#1a1240] hover:bg-white disabled:opacity-40";
const quiet = "rounded-lg border border-white/10 text-[#c9c2e6] hover:bg-white/[0.05] hover:text-white";

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
        <div className="mx-auto max-w-[820px] px-10 pt-12 pb-24">
          <header className="flex items-end justify-between gap-6">
            <h1 className="text-[30px] leading-none font-semibold tracking-tight text-white">Claude accounts</h1>
            <motion.button layout whileTap={{ scale: 0.96 }} onClick={() => setAdding((v) => !v)} className={`flex shrink-0 items-center gap-1.5 px-3 py-1.5 text-xs ${adding ? quiet : primary}`}>
              {adding ? <XIcon className="size-3.5" /> : <PlusIcon className="size-3.5" weight="bold" />}
              {adding ? "Close" : "Add account"}
            </motion.button>
          </header>

          <AnimatePresence initial={false}>
            {adding && (
              <motion.div key="add" initial={{ height: 0, opacity: 0 }} animate={{ height: "auto", opacity: 1 }} exit={{ height: 0, opacity: 0 }} transition={{ duration: 0.28, ease }} className="overflow-hidden">
                <AddAccount
                  onDone={() => {
                    setAdding(false);
                    d.mutateAccounts(() => api.accounts());
                  }}
                />
              </motion.div>
            )}
            {d.error && (
              <motion.p key="error" initial={{ opacity: 0, y: -4 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} className="mt-6 rounded-lg border border-[#ff9fbf]/30 bg-[#ff9fbf]/[0.06] px-4 py-2.5 text-[13px] text-[#ffb8cf]">
                {d.error}
              </motion.p>
            )}
          </AnimatePresence>

          <Section title="Connected" count={connected.length}>
            {connected.length === 0 && <Empty key="empty">No connected accounts. Add one above, or connect one detected on a device.</Empty>}
            {connected.map((a, i) => (
              <AccountRow key={a.id} index={i} account={a}>
                <div className="flex items-center gap-3">
                  <Toggle checked={a.enabled} onChange={(v) => d.mutateAccounts(() => api.setEnabled(a.id, v))} label="Use for automatic switching" />
                  <div className="flex flex-col">
                    <IconButton disabled={i === 0} onClick={() => move(i, -1)} label="Move up" icon={<CaretUpIcon className="size-3" weight="bold" />} />
                    <IconButton disabled={i === connected.length - 1} onClick={() => move(i, 1)} label="Move down" icon={<CaretDownIcon className="size-3" weight="bold" />} />
                  </div>
                  <Disconnect shared={isShared(a)} onConfirm={() => d.mutateAccounts(() => api.disconnect(a.id))} />
                </div>
              </AccountRow>
            ))}
          </Section>

          <Resets accounts={connected} />

          <AnimatePresence initial={false}>
            {(detected.length > 0 || d.accounts.length === 0) && (
              <Section key="detected" title="Detected on your devices" count={detected.length}>
                {d.accounts.length === 0 && <Empty key="empty">No accounts detected. Sign in to Claude Code on a device running nebula sync.</Empty>}
                {detected.map((a, i) => (
                  <AccountRow key={a.id} index={i} account={a}>
                    <motion.button
                      whileTap={{ scale: 0.96 }}
                      onClick={() => d.mutateAccounts(() => api.connect(a.id))}
                      className="rounded-lg px-3 py-1.5 text-xs font-medium text-[#c9b8ff] ring-1 ring-[#a98bff]/40 hover:bg-[#a98bff]/10"
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

function Section({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  return (
    <motion.section layout="position" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="mt-12">
      <h2 className="mb-6 flex items-baseline gap-2 border-b border-white/[0.07] pb-3 text-sm font-medium text-white">
        {title}
        <AnimatePresence mode="popLayout" initial={false}>
          <motion.span key={count} initial={{ opacity: 0, y: 4 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -4 }} className="tabular text-xs font-normal text-[#8b84ad]">
            {count}
          </motion.span>
        </AnimatePresence>
      </h2>
      <div className="space-y-10">
        <AnimatePresence initial={false} mode="popLayout">
          {children}
        </AnimatePresence>
      </div>
    </motion.section>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return (
    <motion.p layout initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="text-[13px] leading-relaxed text-[#8b84ad]">
      {children}
    </motion.p>
  );
}

function Tag({ children, tone = "plain" }: { children: React.ReactNode; tone?: "plain" | "shared" | "limited" }) {
  const tones = {
    plain: "border-white/10 text-[#c9c2e6]",
    shared: "border-[#8fe3bb]/30 text-[#8fe3bb]",
    limited: "border-[#ffc9dc]/40 text-[#ffc9dc]",
  };
  return <span className={`rounded-md border px-1.5 py-px text-[10px] ${tones[tone]}`}>{children}</span>;
}

function AccountRow({ account: a, index, children }: { account: Account; index: number; children: React.ReactNode }) {
  const name = a.display_name || a.email || a.id.slice(0, 8);
  const p = plan(a);
  const stale = a.usage ? Date.now() - Date.parse(a.usage.observed_at) > STALE_MS : true;
  const shared = isShared(a);
  const devices = a.sightings.filter((s) => s.device_id !== SHARED_DEVICE);
  const source = (id: string) => (id === SHARED_DEVICE ? "server" : fmt.short(id));
  const connected = a.state === "connected";
  return (
    <motion.article
      layout
      layoutId={`account-${a.id}`}
      {...stagger(index)}
      exit={{ opacity: 0, scale: 0.97, transition: { duration: 0.15 } }}
      className="grid grid-cols-[2.5rem_1fr] gap-x-4"
    >
      <span className="tabular pt-0.5 text-2xl leading-none font-semibold text-white/25">{connected ? a.priority : ""}</span>
      <div className="min-w-0">
        <div className="flex items-start gap-4">
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="truncate text-[15px] font-medium text-white">{name}</span>
              {p && <Tag>{p}</Tag>}
              {shared && <Tag tone="shared">All devices</Tag>}
              {connected && !a.enabled && <Tag>Paused</Tag>}
              {a.limited_until && Date.parse(a.limited_until) > Date.now() && (
                <Tag tone="limited">Limited until {new Date(a.limited_until).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" })}</Tag>
              )}
            </div>
            <div className="mt-0.5 truncate text-xs text-[#8b84ad]">
              {a.display_name && a.email ? `${a.email}, ` : ""}
              {shared
                ? `shared from the server, on ${devices.length} ${devices.length === 1 ? "device" : "devices"}`
                : a.sightings.length
                  ? `signed in on ${a.sightings.map((s) => `${fmt.short(s.device_id)} (${s.profile})`).join(", ")}, reported ${fmt.ago(a.sightings[0].reported_at)}`
                  : "not signed in on any device"}
            </div>
          </div>
          {children}
        </div>
        {a.usage?.limits.length ? (
          <>
            <div className="mt-4 grid gap-x-8 gap-y-4 sm:grid-cols-2">
              {a.usage.limits.map((l) => (
                <Meter key={l.kind} limit={l} stale={stale} />
              ))}
            </div>
            <p className="mt-3 text-[11px] text-[#8b84ad]">
              {stale ? "Stale. " : ""}Usage as of {fmt.ago(a.usage.observed_at)} from {source(a.usage.device_id)}
            </p>
          </>
        ) : (
          connected && <p className="mt-3 text-xs text-[#8b84ad]">Usage unknown</p>
        )}
      </div>
    </motion.article>
  );
}

const severity: Record<string, { bar: string; text: string; label: string }> = {
  warning: { bar: "bg-[#ffc9dc]", text: "text-[#ffc9dc]", label: "Near limit" },
  critical: { bar: "bg-[#ff9fbf]", text: "text-[#ff9fbf]", label: "At limit" },
  exhausted: { bar: "bg-[#ff9fbf]", text: "text-[#ff9fbf]", label: "At limit" },
};

function Meter({ limit: l, stale }: { limit: Limit; stale: boolean }) {
  const s = l.severity ? severity[l.severity] : undefined;
  const pct = Math.min(100, Math.max(0, l.percent));
  return (
    <div>
      <div className="flex items-baseline justify-between text-xs">
        <span className="text-[#c9c2e6]">
          {limitLabel(l)}
          {s && (
            <span className={`ml-2 inline-flex items-center gap-1 text-[11px] ${s.text}`}>
              <WarningIcon className="size-3" />
              {s.label}
            </span>
          )}
        </span>
        <span className={`tabular text-sm font-medium ${stale ? "text-[#8b84ad]" : s ? s.text : "text-white"}`}>{Math.round(l.percent)}%</span>
      </div>
      <div className="mt-1.5 h-[3px] rounded-full bg-white/[0.06]" role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label={`${limitLabel(l)} usage`}>
        <motion.div
          className={`h-full rounded-full ${stale ? "bg-white/25" : s ? s.bar : "edge-x"}`}
          initial={{ width: 0 }}
          animate={{ width: `${pct}%` }}
          transition={{ duration: 0.7, ease, delay: 0.1 }}
        />
      </div>
      <div className="mt-1 text-[11px] text-[#8b84ad]">{until(l.resets_at)}</div>
    </div>
  );
}

function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <label className="flex cursor-pointer items-center gap-2 text-xs text-[#8b84ad]" title={label}>
      <span>Auto-switch</span>
      <button
        role="switch"
        aria-checked={checked}
        aria-label={label}
        onClick={() => onChange(!checked)}
        className={`relative h-[18px] w-8 rounded-full transition-colors ${checked ? "bg-[#a98bff]" : "bg-white/[0.12]"}`}
      >
        <motion.span className="absolute top-[2px] left-[2px] size-[14px] rounded-full bg-white shadow" animate={{ x: checked ? 14 : 0 }} />
      </button>
    </label>
  );
}

function IconButton({ icon, label, onClick, disabled }: { icon: React.ReactNode; label: string; onClick: () => void; disabled?: boolean }) {
  return (
    <button onClick={onClick} disabled={disabled} aria-label={label} title={label} className="grid h-4 w-6 place-items-center rounded text-[#8b84ad] hover:bg-white/[0.06] hover:text-white disabled:opacity-25 disabled:hover:bg-transparent">
      {icon}
    </button>
  );
}

function Disconnect({ shared, onConfirm }: { shared: boolean; onConfirm: () => void }) {
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
        <motion.button
          key="confirm"
          {...swap}
          onClick={onConfirm}
          className="rounded-lg border border-[#ff9fbf]/40 bg-[#ff9fbf]/10 px-3 py-1.5 text-xs text-[#ffb8cf] hover:bg-[#ff9fbf]/20"
          title={shared ? "Signs every device out of this account." : "Removes its priority and preferences. Devices stay signed in."}
        >
          Confirm disconnect
        </motion.button>
      ) : (
        <motion.button key="ask" {...swap} onClick={() => setAsking(true)} className={`px-3 py-1.5 text-xs ${quiet}`}>
          Disconnect
        </motion.button>
      )}
    </AnimatePresence>
  );
}

const clearLabel = (c: string) =>
  c === "five_hour" ? "Session" : c === "seven_day" ? "Weekly" : c.startsWith("seven_day_") ? `Weekly ${c.slice(10).replace(/_/g, " ")}` : c.replace(/_/g, " ");
const openReset = (r: Reset) => r.state === "pending" || r.state === "approved" || r.state === "executing";

// Reset offers each account reported, with a way to spend one. Using a reset
// takes a fresh passkey sign-in for that request; a device holding the account
// then redeems it with the account's own token.
function Resets({ accounts }: { accounts: Account[] }) {
  const [list, setList] = useState<Reset[]>([]);
  const [error, setError] = useState(() =>
    new URLSearchParams(location.search).has("signin_error") ? "Passkey verification did not complete, so no reset was used." : "",
  );
  const busy = list.some(openReset);

  useEffect(() => {
    if (location.search) history.replaceState(null, "", "/#accounts");
  }, []);
  useEffect(() => {
    let live = true;
    const load = () => api.resets().then((l) => live && setList(l)).catch(() => {});
    load();
    const t = setInterval(load, busy ? 2000 : 30_000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [busy]);

  const fail = (e: Error) => setError(e.message);
  const verify = (id: string) => api.approveReset(id).then(({ url }) => location.assign(url));
  const use = (a: Account, g: Grant) => {
    setError("");
    api
      .requestReset(a.id, g.id)
      .then((r) => {
        setList((l) => [r, ...l]);
        return verify(r.id);
      })
      .catch(fail);
  };
  const cancel = (id: string) => api.cancelReset(id).then((r) => setList((l) => l.map((x) => (x.id === r.id ? r : x)))).catch(fail);

  const rows = accounts.flatMap((a) => (a.usage?.grants ?? []).map((g) => ({ a, g })));
  if (!accounts.length) return null;
  return (
    <Section title="Resets" count={rows.reduce((n, { g }) => n + g.resets_left, 0)}>
      {error && (
        <motion.p key="error" layout initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="text-[13px] text-[#ffb8cf]">
          {error}
        </motion.p>
      )}
      {!rows.length && <Empty key="empty">No account has a usage reset to spend. Offers appear here as devices report them.</Empty>}
      {rows.map(({ a, g }, i) => {
        const last = list.find((r) => r.account_id === a.id);
        const open = !!last && openReset(last);
        const mine = last?.grant_id === g.id ? last : undefined;
        return (
          <motion.article key={`${a.id}/${g.id}`} layout {...stagger(i)} exit={{ opacity: 0, transition: { duration: 0.15 } }} className="grid grid-cols-[2.5rem_1fr] gap-x-4">
            <span className="tabular pt-0.5 text-2xl leading-none font-semibold text-white/25">{g.resets_left}</span>
            <div className="min-w-0">
              <div className="flex items-start gap-4">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[15px] font-medium text-white">{a.display_name || a.email || a.id.slice(0, 8)}</span>
                    {g.label && <Tag>{g.label}</Tag>}
                    {g.paused && <Tag>Paused</Tag>}
                  </div>
                  <div className="mt-0.5 text-xs text-[#8b84ad]">
                    {g.resets_left} of {g.resets_total} left, clears {g.clears.length ? g.clears.map(clearLabel).join(" and ") : "its limits"}
                    {g.ends_at && `, expires ${new Date(g.ends_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}`}
                    {!g.usable_now && g.resets_left > 0 && (g.needs_limit ? ". Usable once the account reaches a limit" : ". Claude says it is not usable right now")}
                  </div>
                </div>
                <UseReset disabled={open || g.paused || g.resets_left === 0} onConfirm={() => use(a, g)} />
              </div>
              {mine && <ResetStatus reset={mine} onVerify={() => verify(mine.id).catch(fail)} onCancel={() => cancel(mine.id)} />}
            </div>
          </motion.article>
        );
      })}
    </Section>
  );
}

function ResetStatus({ reset: r, onVerify, onCancel }: { reset: Reset; onVerify: () => void; onCancel: () => void }) {
  const text: Record<Reset["state"], string> = {
    pending: "Waiting for your passkey",
    approved: "Approved, waiting for a device holding this account",
    executing: `Being used on ${r.device_id ? fmt.short(r.device_id) : "a device"}`,
    succeeded: "Reset used",
    failed: "Not used",
    cancelled: "Cancelled",
    expired: "Expired",
    unknown: "Unclear whether the reset was used",
  };
  const tone = r.state === "succeeded" ? "text-[#8fe3bb]" : r.state === "failed" || r.state === "unknown" ? "text-[#ffb8cf]" : "text-[#c9c2e6]";
  return (
    <div className="mt-3 flex flex-wrap items-center gap-3 text-xs">
      {openReset(r) && r.state !== "pending" ? <Spinner label={text[r.state]} /> : <span className={tone}>{text[r.state]}{r.message ? `: ${r.message}` : ""}</span>}
      <span className="text-[#8b84ad]">{fmt.ago(r.updated_at)}</span>
      {r.state === "pending" && (
        <button onClick={onVerify} className={`px-3 py-1 ${quiet}`}>
          Verify with passkey
        </button>
      )}
      {(r.state === "pending" || r.state === "approved") && (
        <button onClick={onCancel} className="text-[#8b84ad] hover:text-white">
          Cancel
        </button>
      )}
    </div>
  );
}

function UseReset({ disabled, onConfirm }: { disabled: boolean; onConfirm: () => void }) {
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
        <motion.button
          key="confirm"
          {...swap}
          onClick={() => {
            setAsking(false);
            onConfirm();
          }}
          className={`shrink-0 px-3 py-1.5 text-xs ${primary}`}
          title="Spends one reset once your passkey confirms it. It cannot be undone."
        >
          Verify with passkey
        </motion.button>
      ) : (
        <motion.button key="ask" {...swap} disabled={disabled} onClick={() => setAsking(true)} className={`shrink-0 px-3 py-1.5 text-xs ${quiet} disabled:opacity-40 disabled:hover:bg-transparent`}>
          Use reset
        </motion.button>
      )}
    </AnimatePresence>
  );
}

// Connects an account through Claude's own sign-in, run by the server and
// shared with every device: open the sign-in page, then paste the code it shows.
function AddAccount({ onDone }: { onDone: () => void }) {
  const [signIn, setSignIn] = useState<SignIn>();
  const [code, setCode] = useState("");
  const [error, setError] = useState("");

  const active = signIn && !["completed", "failed", "cancelled", "expired"].includes(signIn.state);
  useEffect(() => {
    if (!signIn || !active) return;
    const t = setInterval(() => api.signIn(signIn.id).then(setSignIn).catch(() => {}), 1000);
    return () => clearInterval(t);
  }, [signIn?.id, active]);

  useEffect(() => {
    if (signIn?.state !== "completed") return;
    const t = setTimeout(onDone, 2600);
    return () => clearTimeout(t);
  }, [signIn?.state, onDone]);

  const run = (op: () => Promise<SignIn>) => {
    setError("");
    op()
      .then(setSignIn)
      .catch((e: Error) => setError(/offline/.test(e.message) ? "The server does not share sign-ins. Start it with --vault (NEBULA_VAULT_DIR)." : e.message));
  };
  const swap = { initial: { opacity: 0, y: 6 }, animate: { opacity: 1, y: 0 }, exit: { opacity: 0, y: -6 }, transition: { duration: 0.18, ease } };

  let body: React.ReactNode;
  let key: string;
  if (!signIn || signIn.state === "cancelled") {
    key = "start";
    body = (
      <div className="flex flex-wrap items-center gap-2">
        <p className="text-[13px] text-[#8b84ad]">The account reaches every device within a minute.</p>
        <motion.button whileTap={{ scale: 0.97 }} onClick={() => run(() => api.startSignIn(SHARED_DEVICE))} className={`ml-auto px-4 py-2 text-[13px] ${primary}`}>
          Sign in with Claude
        </motion.button>
      </div>
    );
  } else if (signIn.state === "pending" || signIn.state === "starting" || signIn.state === "completing") {
    key = signIn.state === "completing" ? "completing" : "starting";
    body = <Spinner label={signIn.state === "completing" ? "Connecting" : "Opening Claude sign-in"} />;
  } else if (signIn.state === "waiting") {
    key = "waiting";
    body = (
      <div className="space-y-4">
        <div className="flex items-center justify-between gap-4">
          <Spinner label="Sign in, then paste the code Claude shows" />
          {signIn.url && (
            <a href={signIn.url} target="_blank" rel="noopener noreferrer" className={`flex shrink-0 items-center gap-1.5 px-3 py-1.5 text-xs ${quiet}`}>
              Open sign-in page
              <ArrowSquareOutIcon className="size-3.5" />
            </a>
          )}
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (code.trim()) run(() => api.submitCode(signIn.id, code.trim()));
          }}
          className="flex items-end gap-2"
        >
          <label className="min-w-0 flex-1">
            <span className="text-xs text-[#c9c2e6]">Code from Claude</span>
            <input
              value={code}
              onChange={(e) => setCode(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              className="mt-1.5 w-full rounded-lg border border-white/10 bg-[#0c0a1d] px-3 py-1.5 font-geist-mono text-xs outline-none focus:border-[#a98bff] focus:ring-2 focus:ring-[#a98bff]/20"
            />
          </label>
          <button disabled={!code.trim()} className={`px-3 py-1.5 text-xs ${primary}`}>
            Connect
          </button>
        </form>
      </div>
    );
  } else if (signIn.state === "completed") {
    key = "completed";
    body = (
      <div className="flex items-center gap-2.5 text-[13px] text-[#e9e5fa]">
        <motion.span initial={{ scale: 0 }} animate={{ scale: 1 }} className="grid size-5 place-items-center rounded-full bg-[#8fe3bb]/20 text-[#8fe3bb]">
          <CheckIcon className="size-3" weight="bold" />
        </motion.span>
        Connected. Reaching every device within a minute.
      </div>
    );
  } else {
    key = "failed";
    body = (
      <div className="flex items-center justify-between gap-4 text-[13px]">
        <span className="text-[#ffb8cf]">{signIn.message || "Sign-in did not complete"}</span>
        <button
          onClick={() => {
            setCode("");
            setSignIn(undefined);
          }}
          className={`shrink-0 px-3 py-1.5 text-xs ${quiet}`}
        >
          Try again
        </button>
      </div>
    );
  }

  return (
    <section className="relative mt-6 overflow-hidden rounded-xl border border-white/[0.08] bg-[#141128]/70 p-5 pl-6">
      <span className="edge absolute top-0 bottom-0 left-0 w-[3px]" />
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-sm font-medium text-white">Connect a Claude account</h2>
        {active && (
          <button onClick={() => run(() => api.cancelSignIn(signIn.id))} className="text-xs text-[#8b84ad] hover:text-white">
            Cancel
          </button>
        )}
      </div>
      <AnimatePresence mode="wait" initial={false}>
        <motion.div key={key} {...swap}>
          {body}
        </motion.div>
      </AnimatePresence>
      {error && <p className="mt-3 text-xs text-[#ffb8cf]">{error}</p>}
    </section>
  );
}

function Spinner({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-2.5 text-[13px] text-[#e9e5fa]">
      <motion.span
        className="size-3.5 rounded-full border-2 border-[#a98bff]/30 border-t-[#c9b8ff]"
        animate={{ rotate: 360 }}
        transition={{ duration: 0.9, repeat: Infinity, ease: "linear" }}
      />
      {label}
    </div>
  );
}
