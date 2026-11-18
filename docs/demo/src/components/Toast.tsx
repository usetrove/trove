import React from "react";
import { Easing, Img, interpolate, staticFile, useCurrentFrame } from "remotion";
import { colors, fonts } from "../theme";

const ease = Easing.bezier(0.16, 1, 0.3, 1);

// A desktop notification, the way Trove announces an Autopilot change
// (internal/autopilot/notify.go). Slides in from the right, holds, slides out.
export const Toast: React.FC<{
  title: string;
  body: string;
  from: number;
  to: number;
  width: number;
  x: number;
  y: number;
}> = ({ title, body, from, to, width, x, y }) => {
  const frame = useCurrentFrame();
  if (frame < from || frame > to + 12) {
    return null;
  }
  const slideIn = interpolate(frame, [from, from + 18], [width + 60, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: ease,
  });
  const slideOut = interpolate(frame, [to, to + 12], [0, width + 60], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: Easing.in(Easing.quad),
  });
  return (
    <div
      style={{
        position: "absolute",
        left: x,
        top: y,
        width,
        translate: `${slideIn + slideOut}px 0px`,
        display: "flex",
        gap: 18,
        alignItems: "flex-start",
        padding: "20px 24px",
        borderRadius: 20,
        background: "rgba(243,245,247,0.96)",
        boxShadow: "0 24px 60px rgba(0,0,0,0.5)",
        fontFamily: fonts.body,
        color: colors.ink,
      }}
    >
      <Img src={staticFile("trove-logo.svg")} style={{ width: 56, height: 56, flexShrink: 0 }} />
      <div style={{ display: "flex", flexDirection: "column", gap: 4, minWidth: 0 }}>
        <div style={{ fontWeight: 700, fontSize: 24 }}>{title}</div>
        <div style={{ fontWeight: 500, fontSize: 21, lineHeight: 1.3, color: colors.inkSoft }}>{body}</div>
      </div>
    </div>
  );
};
