import React from "react";
import { AbsoluteFill, Easing, interpolate, useCurrentFrame } from "remotion";
import { Captions } from "../components/Caption";
import { REPORT } from "../script";
import { useStage } from "../stage";
import { colors, fonts } from "../theme";

export const REPORT_FRAMES = 250;

const ease = Easing.bezier(0.16, 1, 0.3, 1);

const rise = (frame: number, from: number): React.CSSProperties => ({
  opacity: interpolate(frame, [from, from + 12], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  }),
  translate: `0px ${interpolate(frame, [from, from + 20], [26, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: ease,
  })}px`,
});

// A small chip naming how a number was arrived at, the way the report does.
// "measured" is a status (green); "estimated" is neutral, never a warning.
const Tag: React.FC<{ kind: "measured" | "estimated"; size: number }> = ({ kind, size }) => (
  <span
    style={{
      display: "inline-block",
      padding: `${Math.round(size * 0.18)}px ${Math.round(size * 0.6)}px`,
      borderRadius: 999,
      fontFamily: fonts.body,
      fontWeight: 700,
      fontSize: size,
      lineHeight: 1.2,
      color: kind === "measured" ? colors.bg : colors.text,
      background: kind === "measured" ? colors.green : colors.rule,
    }}
  >
    {kind}
  </span>
);

const Tile: React.FC<{
  label: string;
  value: string;
  note: string;
  tag: "measured" | "estimated";
  at: number;
  width: number;
  height: number;
  valueSize: number;
}> = ({ label, value, note, tag, at, width, height, valueSize }) => {
  const frame = useCurrentFrame();
  return (
    <div
      style={{
        width,
        height,
        boxSizing: "border-box",
        padding: "26px 32px",
        borderRadius: 18,
        background: colors.panel,
        border: `1px solid ${colors.rule}`,
        display: "flex",
        flexDirection: "column",
        justifyContent: "space-between",
        ...rise(frame, at),
      }}
    >
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          fontFamily: fonts.body,
          fontWeight: 500,
          fontSize: 28,
          color: colors.muted,
        }}
      >
        <span>{label}</span>
        <Tag kind={tag} size={20} />
      </div>
      <div
        style={{
          fontFamily: fonts.display,
          fontWeight: 800,
          fontSize: valueSize,
          lineHeight: 1,
          letterSpacing: -valueSize * 0.03,
          color: colors.text,
          fontVariantNumeric: "tabular-nums",
        }}
      >
        {value}
      </div>
      <div style={{ fontFamily: fonts.body, fontWeight: 500, fontSize: 24, color: colors.muted }}>
        {note}
      </div>
    </div>
  );
};

// The report's "Biggest leaks" list as one bar per leak. One series, so the
// bars share one colour and need no legend; values sit in text colour.
const Leaks: React.FC<{ at: number; width: number; height: number }> = ({ at, width, height }) => {
  const frame = useCurrentFrame();
  const max = Math.max(...REPORT.leaks.map((l) => l.dollars));
  const rowH = 150;
  const barH = 44;
  return (
    <div
      style={{
        width,
        height,
        boxSizing: "border-box",
        padding: "34px 36px",
        borderRadius: 18,
        background: colors.panel,
        border: `1px solid ${colors.rule}`,
        ...rise(frame, at),
      }}
    >
      <div
        style={{
          fontFamily: fonts.body,
          fontWeight: 700,
          fontSize: 36,
          color: colors.text,
        }}
      >
        Biggest leaks
      </div>
      <div
        style={{
          marginTop: 4,
          fontFamily: fonts.body,
          fontWeight: 500,
          fontSize: 23,
          color: colors.muted,
        }}
      >
        estimated, overlapping, not additive
      </div>
      <div style={{ marginTop: 40, display: "flex", flexDirection: "column", gap: 36 }}>
        {REPORT.leaks.map((leak, i) => {
          const start = at + 24 + i * 14;
          const grow = interpolate(frame, [start, start + 40], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: ease,
          });
          const trackW = width - 72;
          return (
            <div key={leak.name} style={{ height: rowH, opacity: grow > 0 ? 1 : 0 }}>
              <div
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "baseline",
                  fontFamily: fonts.body,
                  fontSize: 28,
                  color: colors.text,
                }}
              >
                <span style={{ fontWeight: 700 }}>{leak.name}</span>
                <span style={{ fontWeight: 500, color: colors.muted, fontSize: 25 }}>
                  {leak.tokens}
                </span>
              </div>
              <div
                style={{
                  marginTop: 16,
                  position: "relative",
                  height: barH,
                  width: trackW,
                  borderRadius: 6,
                  background: colors.rule,
                  overflow: "hidden",
                }}
              >
                <div
                  style={{
                    position: "absolute",
                    left: 0,
                    top: 0,
                    height: barH,
                    width: trackW * (leak.dollars / max) * grow,
                    borderRadius: 6,
                    background: colors.gold,
                  }}
                />
              </div>
              <div
                style={{
                  marginTop: 10,
                  fontFamily: fonts.display,
                  fontWeight: 800,
                  fontSize: 30,
                  color: colors.text,
                  opacity: interpolate(frame, [start + 24, start + 36], [0, 1], {
                    extrapolateLeft: "clamp",
                    extrapolateRight: "clamp",
                  }),
                }}
              >
                {leak.label}
                <span style={{ marginLeft: 12, fontFamily: fonts.body, fontWeight: 500, fontSize: 23, color: colors.muted }}>
                  estimated
                </span>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};

// The report's numbers as a graphic: three headline tiles and the leaks as
// bars. Every figure is one the renderer's golden output prints, with the
// same measured or estimated tag.
export const ReportScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { portrait, caption } = useStage();
  const tiles = [
    { label: "Spend", value: REPORT.spend, note: "at list prices, not an invoice", tag: "estimated" as const },
    { label: "Tokens", value: REPORT.tokens, note: REPORT.period, tag: "measured" as const },
    { label: "Waste", value: REPORT.waste, note: `found, at least; ${REPORT.wasteShare}`, tag: "estimated" as const },
  ];
  const title = (
    <div style={{ position: "absolute", left: portrait ? 66 : 120, top: portrait ? 120 : 70, ...rise(frame, 0) }}>
      <div
        style={{
          fontFamily: fonts.display,
          fontWeight: 800,
          fontSize: portrait ? 64 : 62,
          letterSpacing: -2,
          color: colors.text,
        }}
      >
        Where the tokens leak
      </div>
      <div
        style={{
          marginTop: 6,
          fontFamily: fonts.mono,
          fontSize: portrait ? 26 : 26,
          color: colors.muted,
        }}
      >
        {`trove · ${REPORT.period}`}
      </div>
    </div>
  );
  return (
    <AbsoluteFill style={{ background: colors.bg }}>
      {title}
      {portrait ? (
        <>
          <div style={{ position: "absolute", left: 66, top: 290, display: "flex", gap: 20 }}>
            {tiles.map((t, i) => (
              <Tile key={t.label} {...t} at={10 + i * 10} width={303} height={230} valueSize={60} />
            ))}
          </div>
          <div style={{ position: "absolute", left: 66, top: 560 }}>
            <Leaks at={44} width={948} height={620} />
          </div>
        </>
      ) : (
        <>
          <div
            style={{
              position: "absolute",
              left: 120,
              top: 200,
              display: "flex",
              flexDirection: "column",
              gap: 22,
            }}
          >
            {tiles.map((t, i) => (
              <Tile key={t.label} {...t} at={10 + i * 10} width={640} height={196} valueSize={72} />
            ))}
          </div>
          <div style={{ position: "absolute", left: 820, top: 200 }}>
            <Leaks at={44} width={980} height={632} />
          </div>
        </>
      )}
      <Captions
        items={[
          {
            text: "Every number labeled *measured* or *estimated*. All of it local.",
            from: 30,
            to: 140,
          },
          { text: "You could fix these by hand. *Or not.*", from: 150, to: REPORT_FRAMES },
        ]}
        rect={caption}
      />
    </AbsoluteFill>
  );
};
