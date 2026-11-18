import React from "react";
import { AbsoluteFill, Easing, interpolate, useCurrentFrame } from "remotion";
import { Captions } from "../components/Caption";
import { useStage } from "../stage";
import { colors, fonts } from "../theme";

export const LIFECYCLE_FRAMES = 290;

const ease = Easing.bezier(0.16, 1, 0.3, 1);

// The states an optimizer moves through under Autopilot (internal/autopilot),
// with the reasons `trove autopilot` prints for each and the dates the status
// scenes show. The pill is the optimizer; it arrives at each state in turn.
const STAGES = [
  { name: "shadow", reason: "collecting shadow estimates", date: "2026-10-14", arrive: 20 },
  {
    name: "candidate",
    reason: "evidence looks good; confirming on the next check",
    date: "2026-10-27",
    arrive: 90,
  },
  {
    name: "on",
    reason: "turned on from shadow evidence (estimated)",
    date: "2026-10-28",
    arrive: 150,
  },
];
const GUARD_AT = 205;

const Card: React.FC<{
  stage: (typeof STAGES)[number];
  index: number;
  width: number;
  height: number;
  x: number;
  y: number;
}> = ({ stage, index, width, height, x, y }) => {
  const frame = useCurrentFrame();
  const here = frame >= stage.arrive;
  const isOn = stage.name === "on";
  const edge = here ? (isOn ? colors.green : colors.muted) : colors.rule;
  return (
    <div
      style={{
        position: "absolute",
        left: x,
        top: y,
        width,
        height,
        boxSizing: "border-box",
        padding: "26px 30px",
        borderRadius: 20,
        background: colors.panel,
        border: `3px solid ${edge}`,
        boxShadow: here && isOn ? `0 0 40px ${colors.green}55` : "none",
        display: "flex",
        flexDirection: "column",
        gap: 10,
        opacity: interpolate(frame, [index * 8, index * 8 + 12], [0, 1], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
        }),
        translate: `0px ${interpolate(frame, [index * 8, index * 8 + 20], [24, 0], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: ease,
        })}px`,
      }}
    >
      <div
        style={{
          fontFamily: fonts.mono,
          fontWeight: 700,
          fontSize: 40,
          color: here ? (isOn ? colors.green : colors.text) : colors.muted,
        }}
      >
        {stage.name}
      </div>
      <div
        style={{
          fontFamily: fonts.body,
          fontWeight: 500,
          fontSize: 25,
          lineHeight: 1.3,
          color: colors.muted,
        }}
      >
        {stage.reason}
      </div>
      <div
        style={{
          marginTop: "auto",
          fontFamily: fonts.mono,
          fontSize: 23,
          color: here ? colors.text : colors.rule,
          opacity: interpolate(frame, [stage.arrive, stage.arrive + 10], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
          }),
        }}
      >
        {stage.date}
      </div>
    </div>
  );
};

// How Autopilot moves one optimizer from shadow to on, and the guardrail that
// sends it back. Cards sit in a row (landscape) or a column (portrait); the
// pill named after the optimizer travels from card to card.
export const LifecycleScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { portrait, caption } = useStage();

  const cardW = portrait ? 900 : 470;
  const cardH = portrait ? 250 : 290;
  const gap = portrait ? 100 : 110;
  const x0 = portrait ? 90 : 160;
  const y0 = portrait ? 400 : 330;
  const pos = (i: number) =>
    portrait ? { x: x0, y: y0 + i * (cardH + gap) } : { x: x0 + i * (cardW + gap), y: y0 };

  // The pill's centre eases from one card to the next as it is promoted.
  const t = interpolate(
    frame,
    [STAGES[0].arrive, STAGES[1].arrive - 24, STAGES[1].arrive, STAGES[2].arrive - 24, STAGES[2].arrive],
    [0, 0, 1, 1, 2],
    { extrapolateLeft: "clamp", extrapolateRight: "clamp", easing: Easing.inOut(Easing.cubic) },
  );
  const pillW = 360;
  const pillH = 56;
  const pillAt = (i: number) =>
    portrait
      ? { x: pos(i).x + cardW - pillW - 24, y: pos(i).y + cardH - pillH - 22 }
      : { x: pos(i).x + (cardW - pillW) / 2, y: pos(i).y - pillH - 26 };
  const lo = Math.floor(t);
  const hi = Math.min(2, lo + 1);
  const f = t - lo;
  const pillX = pillAt(lo).x + (pillAt(hi).x - pillAt(lo).x) * f;
  const pillY = pillAt(lo).y + (pillAt(hi).y - pillAt(lo).y) * f;
  const onNow = frame >= STAGES[2].arrive;

  // The guardrail: a line from "on" back to "shadow", drawn on at GUARD_AT.
  const guardDraw = interpolate(frame, [GUARD_AT, GUARD_AT + 40], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: ease,
  });
  const path = portrait
    ? (() => {
        // Down out of "on", along under it, up the right side, into "shadow".
        const sx = pos(2).x + 70;
        const sy = pos(2).y + cardH;
        const by = sy + 54;
        const bx = pos(2).x + cardW + 46;
        const ex = pos(0).x + cardW;
        const ey = pos(0).y + cardH / 2;
        return { d: `M${sx} ${sy} V${by} H${bx} V${ey} H${ex + 16}`, lx: pos(2).x + cardW / 2, ly: by };
      })()
    : (() => {
        const sx = pos(2).x + cardW / 2;
        const sy = pos(2).y + cardH;
        const ex = pos(0).x + cardW / 2;
        const ey = pos(0).y + cardH;
        const by = sy + 90;
        return { d: `M${sx} ${sy} V${by} H${ex} V${ey + 16}`, lx: (sx + ex) / 2, ly: by };
      })();

  const arrow = (i: number) => {
    const a = pos(i);
    const on = interpolate(frame, [STAGES[i + 1].arrive - 30, STAGES[i + 1].arrive - 10], [0, 1], {
      extrapolateLeft: "clamp",
      extrapolateRight: "clamp",
    });
    return portrait
      ? { x: a.x + cardW / 2 - 18, y: a.y + cardH + (gap - 36) / 2, rot: 90, on }
      : { x: a.x + cardW + (gap - 36) / 2, y: a.y + cardH / 2 - 18, rot: 0, on };
  };

  return (
    <AbsoluteFill style={{ background: colors.bg }}>
      <div
        style={{
          position: "absolute",
          left: portrait ? 90 : 160,
          top: portrait ? 150 : 100,
          width: portrait ? 900 : 1600,
          fontFamily: fonts.display,
          fontWeight: 800,
          fontSize: portrait ? 64 : 62,
          letterSpacing: -2,
          color: colors.text,
          opacity: interpolate(frame, [0, 12], [0, 1], { extrapolateRight: "clamp" }),
        }}
      >
        How Autopilot decides
        <div
          style={{
            marginTop: 6,
            fontFamily: fonts.body,
            fontWeight: 500,
            fontSize: portrait ? 25 : 27,
            letterSpacing: 0,
            color: colors.muted,
          }}
        >
          Only from shadow evidence. Only at session boundaries. Never over a setting you made.
        </div>
      </div>

      <svg
        width={portrait ? 1080 : 1920}
        height={portrait ? 1920 : 1080}
        style={{ position: "absolute", left: 0, top: 0 }}
      >
        <path
          d={path.d}
          fill="none"
          stroke={colors.amber}
          strokeWidth={4}
          strokeLinecap="round"
          strokeLinejoin="round"
          pathLength={1}
          strokeDasharray="1"
          strokeDashoffset={1 - guardDraw}
        />
      </svg>
      {guardDraw > 0.98 ? (
        <div
          style={{
            position: "absolute",
            left: path.lx,
            top: path.ly,
            translate: "-50% -50%",
            padding: "8px 18px",
            borderRadius: 999,
            background: colors.amber,
            color: colors.bg,
            fontFamily: fonts.body,
            fontWeight: 700,
            fontSize: 24,
            whiteSpace: "nowrap",
          }}
        >
          guardrail: back to shadow if it stops paying off
        </div>
      ) : null}

      {STAGES.map((s, i) => (
        <Card key={s.name} stage={s} index={i} width={cardW} height={cardH} {...pos(i)} />
      ))}
      {[0, 1].map((i) => {
        const a = arrow(i);
        return (
          <div
            key={i}
            style={{
              position: "absolute",
              left: a.x,
              top: a.y,
              width: 36,
              height: 36,
              rotate: `${a.rot}deg`,
              opacity: a.on,
              color: colors.muted,
              fontSize: 36,
              lineHeight: "36px",
              textAlign: "center",
              fontFamily: fonts.body,
            }}
          >
            {"→"}
          </div>
        );
      })}

      <div
        style={{
          position: "absolute",
          left: pillX,
          top: pillY,
          width: pillW,
          height: pillH,
          boxSizing: "border-box",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          borderRadius: 999,
          background: onNow ? colors.gold : colors.text,
          color: colors.bg,
          fontFamily: fonts.mono,
          fontWeight: 700,
          fontSize: 26,
          boxShadow: onNow ? `0 0 30px ${colors.gold}80` : "0 10px 30px rgba(0,0,0,0.4)",
          opacity: interpolate(frame, [STAGES[0].arrive, STAGES[0].arrive + 10], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
          }),
        }}
      >
        noise_normalizer
      </div>

      <Captions
        items={[
          { text: "Shadow, then candidate, then on. Every step *logged*.", from: 30, to: 190 },
          {
            text: "Stops paying off? A guardrail rolls it back. *No hands.*",
            from: 200,
            to: LIFECYCLE_FRAMES,
          },
        ]}
        rect={caption}
      />
    </AbsoluteFill>
  );
};
