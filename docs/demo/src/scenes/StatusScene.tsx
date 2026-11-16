import React from "react";
import { AbsoluteFill, Easing, interpolate, useCurrentFrame } from "remotion";
import { Captions } from "../components/Caption";
import { useStage } from "../stage";
import { colors, fonts } from "../theme";

export const STATUS_FRAMES = 98;

// The one-line status Trove prints for an agent's status line, in the format
// of internal/report.RenderStatus ("~" marks an estimate). The spend matches
// the report scene; the saved figure ticks up as the session goes on.
export const StatusScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { portrait, caption } = useStage();
  const saved = interpolate(frame, [34, 56], [0.4, 0.5], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: Easing.bezier(0.16, 1, 0.3, 1),
  });
  const boxW = portrait ? 960 : 1500;
  const mono = portrait ? 27 : 32;
  const appear = interpolate(frame, [0, 14], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  const cursorOn = Math.floor(frame / 16) % 2 === 0;
  return (
    <AbsoluteFill style={{ background: colors.bg }}>
      <div
        style={{
          position: "absolute",
          left: "50%",
          top: portrait ? 560 : 300,
          translate: "-50% 0px",
          width: boxW,
          opacity: appear,
          fontFamily: fonts.mono,
          fontSize: mono,
          color: colors.text,
        }}
      >
        <div style={{ color: colors.muted, marginBottom: 22, whiteSpace: "pre" }}>
          {"● Ran 3 tests, all passing. Want me to add the edge case for empty carts?"}
        </div>
        <div
          style={{
            border: `1px solid ${colors.rule}`,
            borderRadius: 12,
            padding: "22px 26px",
            background: colors.panel,
            whiteSpace: "pre",
          }}
        >
          <span style={{ color: colors.green, fontWeight: 700 }}>{"> "}</span>
          <span style={{ color: colors.muted }}>{"yes, and keep the fixtures small"}</span>
          {cursorOn ? (
            <span
              style={{
                display: "inline-block",
                width: "0.6em",
                height: "1.1em",
                verticalAlign: "-0.2em",
                background: colors.text,
                marginLeft: 2,
              }}
            />
          ) : null}
        </div>
        <div
          style={{
            marginTop: 18,
            paddingLeft: 6,
            color: colors.muted,
            whiteSpace: "pre",
            fontSize: mono * 0.95,
          }}
        >
          {"~$73.50 this month · session ~$3.20 · "}
          <span
            style={{
              color: frame > 40 ? colors.green : colors.muted,
              fontWeight: frame > 40 ? 700 : 400,
            }}
          >
            {`saved ~$${saved.toFixed(2)}`}
          </span>
        </div>
      </div>
      <Captions
        items={[
          { text: "The status line keeps count inside your agent.", from: 10, to: 96 },
        ]}
        rect={caption}
      />
    </AbsoluteFill>
  );
};
