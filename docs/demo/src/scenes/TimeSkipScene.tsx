import React from "react";
import { AbsoluteFill, Easing, interpolate, useCurrentFrame } from "remotion";
import { Captions } from "../components/Caption";
import { useStage } from "../stage";
import { colors, fonts } from "../theme";

export const TIMESKIP_FRAMES = 130;

// Autopilot's v1 policy needs at least ten shadow sessions before it turns
// anything on (internal/autopilot/autopilot.go, DefaultPolicy). The counter
// runs up to that number.
const SESSIONS = 10;

const ease = Easing.bezier(0.16, 1, 0.3, 1);

export const TimeSkipScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { portrait, caption } = useStage();
  const count = Math.round(
    interpolate(frame, [10, 74], [1, SESSIONS], {
      extrapolateLeft: "clamp",
      extrapolateRight: "clamp",
      easing: Easing.out(Easing.cubic),
    }),
  );
  const pop = interpolate(frame, [0, 18], [0.6, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: ease,
  });
  const appear = interpolate(frame, [0, 10], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  const num = portrait ? 300 : 300;
  return (
    <AbsoluteFill style={{ background: colors.bg }}>
      <div
        style={{
          position: "absolute",
          left: "50%",
          top: portrait ? 520 : 150,
          translate: "-50% 0px",
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          gap: portrait ? 26 : 10,
          opacity: appear,
          scale: String(pop),
        }}
      >
        <div
          style={{
            fontFamily: fonts.display,
            fontWeight: 800,
            fontSize: num,
            lineHeight: 1,
            letterSpacing: -10,
            color: colors.text,
            fontVariantNumeric: "tabular-nums",
          }}
        >
          {count}
        </div>
        <div
          style={{
            fontFamily: fonts.display,
            fontWeight: 700,
            fontSize: portrait ? 72 : 76,
            color: colors.muted,
            letterSpacing: -1,
          }}
        >
          sessions later
        </div>
        <div style={{ display: "flex", gap: 14, marginTop: portrait ? 30 : 22 }}>
          {Array.from({ length: SESSIONS }, (_, i) => {
            const lit = i < count;
            return (
              <div
                key={i}
                style={{
                  width: portrait ? 66 : 72,
                  height: 14,
                  borderRadius: 7,
                  background: lit ? colors.gold : colors.rule,
                  boxShadow: lit ? `0 0 18px ${colors.gold}80` : "none",
                }}
              />
            );
          })}
        </div>
      </div>
      <Captions
        items={[
          {
            text: "Trove watched every one in shadow. Estimated everything. *Changed nothing.*",
            from: 50,
            to: TIMESKIP_FRAMES,
          },
        ]}
        rect={caption}
      />
    </AbsoluteFill>
  );
};
