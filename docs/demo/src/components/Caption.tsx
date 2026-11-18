import React from "react";
import { Easing, interpolate, useCurrentFrame } from "remotion";
import { colors, fonts } from "../theme";
import type { Rect } from "../stage";

// Text between asterisks is set in the accent colour: "Run *trove* now".
export type CaptionItem = { text: string; from: number; to: number };

const ease = Easing.bezier(0.16, 1, 0.3, 1);

const spans = (text: string, accent: string): React.ReactNode[] =>
  text.split("*").map((part, i) =>
    i % 2 === 1 ? (
      <span key={i} style={{ color: accent, fontWeight: 700 }}>
        {part}
      </span>
    ) : (
      <React.Fragment key={i}>{part}</React.Fragment>
    ),
  );

export const Captions: React.FC<{
  items: CaptionItem[];
  rect: Rect & { font: number };
  color?: string;
  accent?: string;
}> = ({ items, rect, color = colors.text, accent = colors.gold }) => {
  const frame = useCurrentFrame();
  return (
    <div
      style={{
        position: "absolute",
        left: rect.x,
        top: rect.y,
        width: rect.w,
        height: rect.h,
      }}
    >
      {items.map((c, i) => {
        if (frame < c.from || frame > c.to) {
          return null;
        }
        return (
          <div
            key={i}
            style={{
              position: "absolute",
              inset: 0,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              textAlign: "center",
              fontFamily: fonts.body,
              fontWeight: 500,
              fontSize: rect.font,
              lineHeight: 1.25,
              color,
              opacity: interpolate(frame, [c.from, c.from + 8, c.to - 6, c.to], [0, 1, 1, 0], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
              }),
              translate: `0px ${interpolate(frame, [c.from, c.from + 14], [22, 0], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: ease,
              })}px`,
            }}
          >
            <span>{spans(c.text, accent)}</span>
          </div>
        );
      })}
    </div>
  );
};
