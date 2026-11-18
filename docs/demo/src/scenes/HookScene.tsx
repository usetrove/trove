import React from "react";
import { Easing, Img, interpolate, staticFile, useCurrentFrame } from "remotion";
import { Paper } from "../components/Paper";
import { useStage } from "../stage";
import { colors, fonts } from "../theme";

export const HOOK_FRAMES = 130;

const ease = Easing.bezier(0.16, 1, 0.3, 1);

// One word popping into place.
const Word: React.FC<{ text: string; at: number; color?: string }> = ({ text, at, color }) => {
  const frame = useCurrentFrame();
  return (
    <span
      style={{
        display: "inline-block",
        color,
        opacity: interpolate(frame, [at, at + 7], [0, 1], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
        }),
        scale: String(
          interpolate(frame, [at, at + 16], [0.72, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: ease,
          }),
        ),
        translate: `0px ${interpolate(frame, [at, at + 16], [26, 0], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: ease,
        })}px`,
      }}
    >
      {text}
    </span>
  );
};

const Line: React.FC<{
  words: string[];
  start: number;
  step: number;
  size: number;
  color: string;
  weight: number;
}> = ({ words, start, step, size, color, weight }) => (
  <div
    style={{
      display: "flex",
      flexWrap: "wrap",
      justifyContent: "center",
      columnGap: size * 0.26,
      fontFamily: fonts.display,
      fontWeight: weight,
      fontSize: size,
      lineHeight: 1.08,
      letterSpacing: -size * 0.025,
    }}
  >
    {words.map((w, i) => (
      <Word key={i} text={w} at={start + i * step} color={color} />
    ))}
  </div>
);

export const HookScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { portrait } = useStage();
  const big = portrait ? 116 : 124;
  // A soft glow that drifts behind the words, so the card is never static.
  const drift = interpolate(frame, [0, HOOK_FRAMES], [-60, 60]);
  return (
    <Paper>
      <div
        style={{
          position: "absolute",
          left: "50%",
          top: "50%",
          width: portrait ? 1100 : 1500,
          height: portrait ? 1100 : 900,
          translate: `calc(-50% + ${drift}px) -50%`,
          background: `radial-gradient(closest-side, ${colors.inkGold}2a, transparent 70%)`,
          filter: "blur(40px)",
        }}
      />
      <div
        style={{
          position: "absolute",
          left: "50%",
          top: "50%",
          width: portrait ? 900 : 1200,
          height: portrait ? 900 : 700,
          translate: `calc(-50% - ${drift * 1.4}px) calc(-50% + ${drift * 0.6}px)`,
          background: `radial-gradient(closest-side, ${colors.inkGold}1f, transparent 70%)`,
          filter: "blur(40px)",
        }}
      />
      <div
        style={{
          position: "relative",
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          textAlign: "center",
          gap: portrait ? 48 : 36,
          maxWidth: portrait ? 980 : 1700,
          padding: "0 60px",
        }}
      >
        <Img
          src={staticFile("trove-logo.svg")}
          style={{
            width: portrait ? 220 : 150,
            height: portrait ? 220 : 150,
            opacity: interpolate(frame, [0, 10], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
            }),
            scale: String(
              interpolate(frame, [0, 20], [0.5, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: ease,
              }),
            ),
          }}
        />
        <div style={{ display: "flex", flexDirection: "column", gap: portrait ? 20 : 10 }}>
          <Line
            words={["Your", "agent", "is", "leaking", "tokens.", "Right", "now."]}
            start={8}
            step={7}
            size={big}
            color={colors.ink}
            weight={800}
          />
          <Line
            words={["Trove", "fixes", "it.", "Automatically."]}
            start={66}
            step={8}
            size={big}
            color={colors.inkGold}
            weight={800}
          />
        </div>
      </div>
    </Paper>
  );
};
