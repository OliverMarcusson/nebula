import { useEffect, useState, type FormEvent } from "react";
import { AnimatePresence, motion } from "motion/react";
import { api, AuthError, token } from "./api";
import { ease } from "./motion";
import { LockKeyIcon } from "@phosphor-icons/react";

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
    <main className="relative flex min-h-dvh items-center justify-center overflow-hidden bg-[#0c0a1d] p-6 font-geist text-[#e9e5fa]">
      <div aria-hidden className="glow" />
      <motion.div
        initial={{ opacity: 0, y: 12, scale: 0.98 }}
        animate={error ? { opacity: 1, y: 0, scale: 1, x: [0, -6, 6, -3, 3, 0] } : { opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.35, ease }}
        className="relative w-full max-w-sm rounded-xl border border-white/[0.08] bg-[#141128]/80 p-7 shadow-[0_30px_80px_-20px_rgb(5_3_20/0.8)] backdrop-blur-xl"
      >
        <div className="flex flex-col items-start gap-4">
          <img src="/icon.png" alt="" className="size-11" />
          <h1 className="text-[22px] leading-tight font-semibold tracking-tight text-white">Sign in to Nebula</h1>
        </div>

        {claustra && (
          <a
            href="/auth/login"
            className="mt-6 flex w-full items-center justify-center gap-2 rounded-lg bg-[#e9e3ff] py-2.5 text-sm font-medium text-[#1a1240] transition-colors hover:bg-white active:scale-[0.98]"
          >
            Log in with Claustra
            <LockKeyIcon className="size-4" weight="bold" aria-hidden />
          </a>
        )}

        <AnimatePresence>
          {error && (
            <motion.p initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="overflow-hidden pt-3 text-sm text-[#ff9fbf]">
              {error}
            </motion.p>
          )}
        </AnimatePresence>

        {claustra && !useToken && (
          <button onClick={() => setUseToken(true)} className="mt-4 w-full text-center text-xs text-[#8b84ad] hover:text-white">
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
              {claustra && <div className="my-5 border-t border-white/[0.07]" />}
              <label className={claustra ? "block" : "mt-6 block"}>
                <span className="text-[13px] text-[#c9c2e6]">Device token</span>
                <input
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus={!claustra}
                  value={value}
                  onChange={(e) => setValue(e.target.value)}
                  className="mt-1.5 w-full rounded-lg border border-white/10 bg-[#0c0a1d] px-3 py-2 font-geist-mono text-sm outline-none placeholder:text-[#8b84ad] focus:border-[#a98bff] focus:ring-2 focus:ring-[#a98bff]/25"
                  placeholder="contents of device.token"
                />
              </label>
              <button
                type="submit"
                disabled={busy || !value.trim()}
                className={`mt-4 w-full rounded-lg py-2 text-sm font-medium disabled:opacity-50 ${claustra ? "border border-white/10 text-[#c9c2e6] hover:bg-white/[0.05] hover:text-white" : "bg-[#e9e3ff] text-[#1a1240] hover:bg-white"}`}
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
