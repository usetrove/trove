import React from "react";
import { Easing, Img, interpolate, staticFile, useCurrentFrame } from "remotion";
import { Paper } from "../components/Paper";
import { useStage } from "../stage";
import { colors, fonts } from "../theme";

export const TITLE_FRAMES = 63;

const ease = Easing.bezier(0.16, 1, 0.3, 1);

const rise = (frame: number, from: number): React.CSSProperties => ({
  opacity: interpolate(frame, [from, from + 12], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  }),
  translate: `0px ${interpolate(frame, [from, from + 18], [20, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: ease,
  })}px`,
});

export const TitleScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { portrait } = useStage();
  return (
    <Paper>
      <div
        style={{
          display: "flex",
          flexDirection: portrait ? "column" : "row",
          alignItems: "center",
          gap: portrait ? 10 : 36,
          ...rise(frame, 0),
        }}
      >
        <Img
          src={staticFile("trove-logo.svg")}
          style={{ width: portrait ? 300 : 220, height: portrait ? 300 : 220 }}
        />
        <div
          style={{
            fontFamily: fonts.display,
            fontWeight: 800,
            fontSize: portrait ? 150 : 150,
            letterSpacing: -4,
            color: colors.ink,
            lineHeight: 1,
          }}
        >
          Trove
        </div>
      </div>
      <div
        style={{
          marginTop: portrait ? 60 : 44,
          textAlign: "center",
          fontFamily: fonts.body,
          fontWeight: 500,
          fontSize: portrait ? 46 : 46,
          color: colors.ink,
          lineHeight: 1.3,
          maxWidth: portrait ? 900 : 1500,
          ...rise(frame, 8),
        }}
      >
        Runs locally. Sends nothing by default. Open source.
      </div>
      <div
        style={{
          marginTop: 26,
          fontFamily: fonts.mono,
          fontSize: portrait ? 36 : 38,
          color: colors.inkSoft,
          ...rise(frame, 16),
        }}
      >
        github.com/usetrove/trove
      </div>
    </Paper>
  );
};
