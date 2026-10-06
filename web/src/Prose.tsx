import { Fragment } from "react";

// Minimal, safe rendering of transcript text: fenced code blocks, inline code,
// and paragraphs. No HTML is ever interpreted.
export default function Prose({ text, code, inline, className = "" }: { text: string; code: string; inline: string; className?: string }) {
  const parts = text.split(/```[^\n]*\n?/);
  return (
    <div className={className}>
      {parts.map((part, i) =>
        i % 2 === 1 ? (
          <pre key={i} className={code}>
            {part.replace(/\n$/, "")}
          </pre>
        ) : (
          part
            .split(/\n{2,}/)
            .filter((p) => p.trim())
            .map((p, j) => (
              <p key={`${i}-${j}`} className="whitespace-pre-wrap break-words">
                {p.split(/(`[^`\n]+`)/).map((s, k) =>
                  s.length > 2 && s.startsWith("`") && s.endsWith("`") ? (
                    <code key={k} className={inline}>
                      {s.slice(1, -1)}
                    </code>
                  ) : (
                    <Fragment key={k}>{s}</Fragment>
                  ),
                )}
              </p>
            ))
        ),
      )}
    </div>
  );
}
