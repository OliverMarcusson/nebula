import { useEffect, useState, type FormEvent } from "react";
import { AnimatePresence, motion } from "motion/react";
import { api, AuthError, token } from "./api";
import { ease } from "./motion";

const signinErrors: Record<string, string> = {
  denied: "Sign-in was cancelled.",
  "not-allowed": "That Claustra account may not use this Nebula.",
  expired: "The sign-in took too long. Try again.",
  unavailable: "Claustra could not be reached. Try again.",
  invalid: "Claustra's answer could not be verified. Try again.",
};

export default function Login({ onSignedIn }: { onSignedIn: (owner: string) => void }) {
  const [claustra, setClaustra] = useState<boolean>();
  const [useToken, setUseToken] = useState(false);
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.authConfig().then((c) => {
      setClaustra(c.claustra);
      if (!c.claustra) setUseToken(true);
    });
    const reason = new URLSearchParams(location.search).get("signin_error");
    if (reason) {
      setError(signinErrors[reason] ?? "Sign-in failed.");
      history.replaceState(null, "", location.pathname + location.hash);
    }
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const { owner } = await api.me(value.trim());
      token.set(value);
      onSignedIn(owner);
    } catch (err) {
      setError(err instanceof AuthError ? "That token was not accepted." : "Could not reach the Nebula server.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="flex min-h-dvh items-center justify-center bg-[#0e0d14] p-6 font-geist text-[#e8e6f0]">
      <motion.div
        initial={{ opacity: 0, y: 12, scale: 0.98 }}
        animate={error ? { opacity: 1, y: 0, scale: 1, x: [0, -6, 6, -3, 3, 0] } : { opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.35, ease }}
        className="w-full max-w-sm rounded-xl border border-white/[0.08] bg-[#14121c] p-7 shadow-[0_30px_80px_-20px_rgba(0,0,0,0.8)]"
      >
        <div className="flex items-center gap-2.5">
          <img src="/icon.png" alt="" className="size-7" />
          <h1 className="text-lg font-medium tracking-tight">Sign in to Nebula</h1>
        </div>

        {claustra && (
          <a
            href="/auth/login"
            className="mt-6 flex w-full items-center justify-center gap-2.5 rounded-md bg-[#7c5cff] py-2.5 text-sm font-medium text-white transition-colors hover:bg-[#8a6dff]"
          >
            Log in with Claustra
            <svg viewBox="0 0 24 24" className="size-4" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
              <rect x="3" y="11" width="18" height="11" rx="2.5" />
              <path d="M7 11V7a5 5 0 0 1 9.9-1" />
              <circle cx="12" cy="16.5" r="1.1" fill="currentColor" stroke="none" />
            </svg>
          </a>
        )}

        <AnimatePresence>
          {error && (
            <motion.p initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="overflow-hidden pt-3 text-sm text-[#ff7a90]">
              {error}
            </motion.p>
          )}
        </AnimatePresence>

        {claustra && !useToken && (
          <button onClick={() => setUseToken(true)} className="mt-4 w-full text-center text-xs text-[#77738a] hover:text-[#d9d6e4]">
            Use a device token
          </button>
        )}

        <AnimatePresence initial={false}>
          {useToken && (
            <motion.form
              key="token"
              onSubmit={submit}
              initial={claustra ? { opacity: 0, height: 0 } : false}
              animate={{ opacity: 1, height: "auto" }}
              exit={{ opacity: 0, height: 0 }}
              transition={{ duration: 0.25, ease }}
              className="overflow-hidden"
            >
              {claustra && <div className="my-5 border-t border-white/[0.06]" />}
              <label className={claustra ? "block" : "mt-6 block"}>
                <span className="text-[13px] text-[#a19db3]">Device token</span>
                <input
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus={!claustra}
                  value={value}
                  onChange={(e) => setValue(e.target.value)}
                  className="mt-1.5 w-full rounded-md border border-white/10 bg-[#0e0d14] px-3 py-2 font-geist-mono text-sm outline-none focus:border-[#7c5cff] focus:ring-2 focus:ring-[#7c5cff]/25"
                  placeholder="contents of device.token"
                />
              </label>
              <button
                type="submit"
                disabled={busy || !value.trim()}
                className={`mt-4 w-full rounded-md py-2 text-sm font-medium disabled:opacity-50 ${claustra ? "border border-white/10 text-[#d9d6e4] hover:bg-white/5" : "bg-[#7c5cff] text-white hover:bg-[#8a6dff]"}`}
              >
                {busy ? "Checking…" : "Sign in with token"}
              </button>
            </motion.form>
          )}
        </AnimatePresence>
      </motion.div>
    </main>
  );
}
