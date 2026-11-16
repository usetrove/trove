import React from "react";
import { Easing, interpolate, useCurrentFrame } from "remotion";
import { colors, fonts } from "../theme";

export type Kind = "cmd" | "out" | "muted" | "bold" | "add" | "del" | "hunk";

export type Highlight = {
  from: number;
  color: string;
  label?: string;
};

export type TermLine = {
  text: string;
  at: number;
  kind: Kind;
  // Typing speed for cmd lines and answers, in characters per frame.
  cps?: number;
  // Text typed by the user at the end of an output line (a y/N prompt).
  answer?: { text: string; at: number };
  highlight?: Highlight;
};

type Row = {
  text: string;
  kind: Kind;
  first: boolean;
  line: TermLine;
  cursor: boolean;
};

// Wrap one logical line to the column count, breaking at spaces like a
// terminal that is too narrow would.
const wrap = (text: string, cols: number): string[] => {
  if (text.length <= cols) {
    return [text];
  }
  const out: string[] = [];
  let rest = text;
  while (rest.length > cols) {
    let cut = rest.lastIndexOf(" ", cols);
    if (cut <= 0) {
      cut = cols;
    }
    out.push(rest.slice(0, cut));
    rest = rest.slice(cut);
    if (rest.startsWith(" ")) {
      rest = rest.slice(1);
    }
  }
  out.push(rest);
  return out;
};

const PROMPT_LEAD = 10;

const visibleText = (
  line: TermLine,
  frame: number,
): { text: string; typing: boolean; shown: boolean } => {
  const cps = line.cps ?? 2.5;
  if (line.kind === "cmd") {
    if (frame < line.at - PROMPT_LEAD) {
      return { text: "", typing: false, shown: false };
    }
    const chars = Math.max(0, Math.floor((frame - line.at) * cps));
    const text = line.text.slice(0, chars);
    return { text, typing: chars < line.text.length, shown: true };
  }
  if (frame < line.at) {
    return { text: "", typing: false, shown: false };
  }
  if (line.answer && frame >= line.answer.at) {
    const chars = Math.floor((frame - line.answer.at) * cps);
    return {
      text: line.text + line.answer.text.slice(0, chars),
      typing: chars < line.answer.text.length,
      shown: true,
    };
  }
  return { text: line.text, typing: false, shown: true };
};

const kindStyle = (kind: Kind): React.CSSProperties => {
  switch (kind) {
    case "muted":
      return { color: colors.muted };
    case "bold":
      return { color: colors.text, fontWeight: 700 };
    case "add":
      return { color: colors.green };
    case "del":
      return { color: colors.red };
    case "hunk":
      return { color: colors.amber };
    default:
      return { color: colors.text };
  }
};

export const Terminal: React.FC<{
  lines: TermLine[];
  width: number;
  height: number;
  fontSize: number;
  title?: string;
}> = ({ lines, width, height, fontSize, title = "zsh" }) => {
  const frame = useCurrentFrame();
  const pad = 24;
  const chrome = 44;
  const lineHeight = Math.round(fontSize * 1.5);
  const innerW = width - pad * 2;
  const innerH = height - chrome - pad * 2;
  const cols = Math.floor(innerW / (fontSize * 0.6));
  const maxRows = Math.floor(innerH / lineHeight);

  const rows: Row[] = [];
  let lastShown = -1;
  lines.forEach((line, i) => {
    if (visibleText(line, frame).shown) {
      lastShown = i;
    }
  });
  lines.forEach((line, i) => {
    const v = visibleText(line, frame);
    if (!v.shown) {
      return;
    }
    const wantsCursor =
      i === lastShown && (line.kind === "cmd" || line.answer !== undefined);
    const blink = v.typing || Math.floor(frame / 16) % 2 === 0;
    const parts = wrap(v.text, cols);
    parts.forEach((text, j) => {
      rows.push({
        text,
        kind: line.kind,
        first: j === 0,
        line,
        cursor: wantsCursor && blink && j === parts.length - 1,
      });
    });
  });

  const scrolled = Math.max(0, rows.length - maxRows);

  return (
    <div
      style={{
        width,
        height,
        borderRadius: 18,
        background: colors.panel,
        border: `1px solid ${colors.rule}`,
        boxShadow: "0 30px 80px rgba(0,0,0,0.45)",
        overflow: "hidden",
        fontFamily: fonts.mono,
        fontSize,
        lineHeight: `${lineHeight}px`,
      }}
    >
      <div
        style={{
          height: chrome,
          display: "flex",
          alignItems: "center",
          padding: "0 18px",
          gap: 9,
          borderBottom: `1px solid ${colors.rule}`,
          color: colors.muted,
          fontFamily: fonts.body,
          fontSize: 19,
          fontWeight: 500,
        }}
      >
        <span style={{ width: 13, height: 13, borderRadius: 7, background: "#FF5F57" }} />
        <span style={{ width: 13, height: 13, borderRadius: 7, background: "#FEBC2E" }} />
        <span style={{ width: 13, height: 13, borderRadius: 7, background: "#28C840" }} />
        <span style={{ marginLeft: "auto", marginRight: "auto" }}>{title}</span>
        <span style={{ width: 57 }} />
      </div>
      <div style={{ height: innerH, margin: pad, overflow: "hidden" }}>
        <div
          style={{
            translate: `0px ${-scrolled * lineHeight}px`,
            whiteSpace: "pre",
          }}
        >
          {rows.map((row, i) => {
            const reveal =
              row.line.kind === "cmd"
                ? 1
                : interpolate(frame, [row.line.at, row.line.at + 4], [0, 1], {
                    extrapolateLeft: "clamp",
                    extrapolateRight: "clamp",
                  });
            const hl = row.first ? row.line.highlight : undefined;
            const hlOn = hl !== undefined && frame >= hl.from;
            return (
              <div
                key={i}
                style={{
                  position: "relative",
                  height: lineHeight,
                  opacity: reveal,
                  ...kindStyle(row.kind),
                }}
              >
                {hl && hlOn ? (
                  <div
                    style={{
                      position: "absolute",
                      left: -10,
                      top: 0,
                      height: lineHeight,
                      width: `calc(${row.text.length}ch + 20px)`,
                      borderRadius: 8,
                      border: `3px solid ${hl.color}`,
                      background: `${hl.color}1a`,
                      transformOrigin: "left center",
                      opacity: interpolate(frame, [hl.from, hl.from + 8], [0, 1], {
                        extrapolateLeft: "clamp",
                        extrapolateRight: "clamp",
                      }),
                      scale: String(
                        interpolate(frame, [hl.from, hl.from + 12], [0.94, 1], {
                          extrapolateLeft: "clamp",
                          extrapolateRight: "clamp",
                          easing: Easing.bezier(0.16, 1, 0.3, 1),
                        }),
                      ),
                    }}
                  />
                ) : null}
                {hl && hlOn && hl.label ? (
                  <div
                    style={{
                      position: "absolute",
                      left: `calc(${row.text.length}ch + 28px)`,
                      top: Math.round(lineHeight * 0.08),
                      height: Math.round(lineHeight * 0.84),
                      display: "flex",
                      alignItems: "center",
                      padding: "0 14px",
                      borderRadius: 999,
                      background: hl.color,
                      color: colors.bg,
                      fontFamily: fonts.body,
                      fontWeight: 700,
                      fontSize: Math.round(fontSize * 0.8),
                      whiteSpace: "nowrap",
                      opacity: interpolate(frame, [hl.from + 6, hl.from + 14], [0, 1], {
                        extrapolateLeft: "clamp",
                        extrapolateRight: "clamp",
                      }),
                      translate: `${interpolate(frame, [hl.from + 6, hl.from + 16], [12, 0], {
                        extrapolateLeft: "clamp",
                        extrapolateRight: "clamp",
                        easing: Easing.bezier(0.16, 1, 0.3, 1),
                      })}px 0px`,
                    }}
                  >
                    {hl.label}
                  </div>
                ) : null}
                <span style={{ position: "relative" }}>
                  {row.kind === "cmd" && row.first ? (
                    <span style={{ color: colors.green, fontWeight: 700 }}>{"❯ "}</span>
                  ) : null}
                  {row.text}
                  {row.cursor ? (
                    <span
                      style={{
                        display: "inline-block",
                        width: "0.6em",
                        height: "1.1em",
                        verticalAlign: "-0.2em",
                        background: colors.text,
                        marginLeft: 1,
                      }}
                    />
                  ) : null}
                </span>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
};
