import React from "react";
import { Easing, interpolate, useCurrentFrame } from "remotion";
import { colors, fonts } from "../theme";
import type { Rect } from "../stage";

export type CaptionItem = { text: string; from: number; to: number };

export const Captions: React.FC<{
  items: CaptionItem[];
  rect: Rect & { font: number };
  color?: string;
}> = ({ items, rect, color = colors.text }) => {
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
              opacity: interpolate(frame, [c.from, c.from + 10, c.to - 8, c.to], [0, 1, 1, 0], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
              }),
              translate: `0px ${interpolate(frame, [c.from, c.from + 14], [18, 0], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: Easing.bezier(0.16, 1, 0.3, 1),
              })}px`,
            }}
          >
            {c.text}
          </div>
        );
      })}
    </div>
  );
};
