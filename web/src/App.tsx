import { useCallback, useEffect, useState } from "react";
import { MotionConfig } from "motion/react";
import { spring } from "./motion";
import { api, AuthError, token } from "./api";
import { useDashboard } from "./hooks";
import Login from "./Login";
import Console from "./Console";

export default function App() {
  const [owner, setOwner] = useState<string | null>(null);
  const [checking, setChecking] = useState(true);

  // A stored device token or a Claustra session cookie may already sign us in.
  useEffect(() => {
    api
      .me()
      .then((m) => setOwner(m.owner))
      .catch((e) => e instanceof AuthError && token.clear())
      .finally(() => setChecking(false));
  }, []);

  const lost = useCallback(() => setOwner(null), []);

  if (checking) return null;
  return (
    <MotionConfig reducedMotion="user" transition={spring}>
      {owner === null ? <Login onSignedIn={setOwner} /> : <Signed owner={owner} onAuthLost={lost} />}
    </MotionConfig>
  );
}

function Signed({ owner, onAuthLost }: { owner: string; onAuthLost: () => void }) {
  return <Console {...useDashboard(owner, onAuthLost)} />;
}
