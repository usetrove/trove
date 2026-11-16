import React from "react";
import { Easing, Img, interpolate, staticFile, useCurrentFrame } from "remotion";
import { Paper } from "../components/Paper";
import { useStage } from "../stage";
import { colors, fonts } from "../theme";

export const HOOK_FRAMES = 80;

const ease = Easing.bezier(0.16, 1, 0.3, 1);

export const HookScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { portrait } = useStage();
  return (
    <Paper>
      <div
        style={{
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          textAlign: "center",
          gap: portrait ? 36 : 24,
          maxWidth: portrait ? 920 : 1600,
          padding: "0 80px",
        }}
      >
        <Img
          src={staticFile("trove-logo.svg")}
          style={{
            width: portrait ? 300 : 230,
            height: portrait ? 300 : 230,
            opacity: interpolate(frame, [0, 12], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
            }),
            scale: String(
              interpolate(frame, [0, 22], [0.7, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: ease,
              }),
            ),
          }}
        />
        <div
          style={{
            fontFamily: fonts.display,
            fontWeight: 800,
            fontSize: portrait ? 88 : 96,
            lineHeight: 1.08,
            color: colors.ink,
            letterSpacing: -2,
            opacity: interpolate(frame, [8, 20], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
            }),
            translate: `0px ${interpolate(frame, [8, 26], [24, 0], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: ease,
            })}px`,
          }}
        >
          Where do your agent&apos;s tokens go?
        </div>
        <div
          style={{
            fontFamily: fonts.body,
            fontWeight: 500,
            fontSize: portrait ? 44 : 42,
            color: colors.inkSoft,
            opacity: interpolate(frame, [22, 34], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
            }),
            translate: `0px ${interpolate(frame, [22, 40], [18, 0], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: ease,
            })}px`,
          }}
        >
          Trove is the efficiency layer for AI coding agents.
        </div>
      </div>
    </Paper>
  );
};
