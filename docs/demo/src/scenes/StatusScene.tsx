import React from "react";
import { AbsoluteFill, Easing, interpolate, useCurrentFrame } from "remotion";
import { Captions } from "../components/Caption";
import { useStage } from "../stage";
import { colors, fonts } from "../theme";

export const STATUS_FRAMES = 150;

const ease = Easing.bezier(0.16, 1, 0.3, 1);

// The one-line status Trove prints for an agent's status line, in the format
// of internal/report.RenderStatus ("~" marks an estimate) plus the suffix
// autopilotSuffix adds in internal/cli/autopilot.go once Autopilot has an
// optimizer on. The spend matches the report scene; the saved figure ticks up
// as the session goes on.
export const StatusScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { portrait, caption } = useStage();
  const saved = interpolate(frame, [40, 76], [0.42, 0.5], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: ease,
  });
  const boxW = portrait ? 960 : 1500;
  const mono = portrait ? 27 : 32;
  const appear = interpolate(frame, [0, 14], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  const suffixAt = 66;
  const suffixOn = frame >= suffixAt;
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
        <div style={{ color: colors.muted, marginBottom: 22, whiteSpace: "pre-wrap" }}>
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
            whiteSpace: "pre-wrap",
            fontSize: mono * 0.95,
          }}
        >
          {"~$73.50 this month · session ~$3.20 · "}
          <span
            style={{
              color: frame > 46 ? colors.green : colors.muted,
              fontWeight: frame > 46 ? 700 : 400,
            }}
          >
            {`saved ~$${saved.toFixed(2)}`}
          </span>
          {suffixOn ? (
            <span
              style={{
                display: "inline-block",
                color: colors.gold,
                fontWeight: 700,
                opacity: interpolate(frame, [suffixAt, suffixAt + 8], [0, 1], {
                  extrapolateLeft: "clamp",
                  extrapolateRight: "clamp",
                }),
                scale: String(
                  interpolate(frame, [suffixAt, suffixAt + 14], [0.8, 1], {
                    extrapolateLeft: "clamp",
                    extrapolateRight: "clamp",
                    easing: ease,
                  }),
                ),
              }}
            >
              {" · autopilot 1 on"}
            </span>
          ) : null}
        </div>
      </div>
      <Captions
        items={[
          {
            text: "Spend, savings, and what Autopilot is doing, *inside your agent*.",
            from: 12,
            to: STATUS_FRAMES,
          },
        ]}
        rect={caption}
      />
    </AbsoluteFill>
  );
};
