import type { Transition } from "motion/react";

// Shared motion vocabulary: one spring for layout and state, one ease for fades.
export const spring: Transition = { type: "spring", stiffness: 520, damping: 42, mass: 0.7 };
export const ease = [0.22, 1, 0.36, 1] as const;

export const fadeUp = {
  initial: { opacity: 0, y: 6 },
  animate: { opacity: 1, y: 0 },
  exit: { opacity: 0, y: -4 },
  transition: { duration: 0.22, ease },
};

export const fade = {
  initial: { opacity: 0 },
  animate: { opacity: 1 },
  exit: { opacity: 0 },
  transition: { duration: 0.16, ease },
};

// Staggers the first few items of a list in; later ones render immediately so long lists stay cheap.
export const stagger = (i: number, limit = 14) =>
  i < limit
    ? { initial: { opacity: 0, y: 4 }, animate: { opacity: 1, y: 0 }, transition: { duration: 0.24, ease, delay: i * 0.025 } }
    : { initial: false as const };
