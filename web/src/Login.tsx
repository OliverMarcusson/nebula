import { useState, type FormEvent } from "react";
import { AnimatePresence, motion } from "motion/react";
import { api, AuthError, token } from "./api";
import { ease } from "./motion";

export default function Login({ onSignedIn }: { onSignedIn: (owner: string) => void }) {
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

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
      <motion.form
        initial={{ opacity: 0, y: 12, scale: 0.98 }}
        animate={error ? { opacity: 1, y: 0, scale: 1, x: [0, -6, 6, -3, 3, 0] } : { opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.35, ease }}
        onSubmit={submit}
        className="w-full max-w-sm rounded-xl border border-white/[0.08] bg-[#14121c] p-7 shadow-[0_30px_80px_-20px_rgba(0,0,0,0.8)]">
        <div className="flex items-center gap-2.5">
          <span className="grid size-7 place-items-center rounded-md bg-gradient-to-br from-[#8f73ff] to-[#5a3fd6] text-[13px] font-semibold text-white">N</span>
          <h1 className="text-lg font-medium tracking-tight">Sign in to Nebula</h1>
        </div>
        <label className="mt-6 block">
          <span className="text-[13px] text-[#a19db3]">Device token</span>
          <input
            type="password"
            autoComplete="off"
            spellCheck={false}
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value)}
            className="mt-1.5 w-full rounded-md border border-white/10 bg-[#0e0d14] px-3 py-2 font-geist-mono text-sm outline-none focus:border-[#7c5cff] focus:ring-2 focus:ring-[#7c5cff]/25"
            placeholder="contents of device.token"
          />
        </label>
        <AnimatePresence>
          {error && (
            <motion.p initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="overflow-hidden pt-3 text-sm text-[#ff7a90]">
              {error}
            </motion.p>
          )}
        </AnimatePresence>
        <button type="submit" disabled={busy || !value.trim()} className="mt-5 w-full rounded-md bg-[#7c5cff] py-2 text-sm font-medium text-white hover:bg-[#8a6dff] disabled:opacity-50">
          {busy ? "Checking…" : "Sign in"}
        </button>
      </motion.form>
    </main>
  );
}
