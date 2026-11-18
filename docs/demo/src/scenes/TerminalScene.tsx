import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { Captions } from "../components/Caption";
import { Terminal } from "../components/Terminal";
import { Toast } from "../components/Toast";
import { partConsent, partInstall, partStatus, TOAST } from "../script";
import type { Part } from "../script";
import { useStage } from "../stage";
import { colors } from "../theme";

// Install, then the report's headline.
export const TERMINAL_INSTALL_FRAMES = partInstall.frames + 8;
// Saying yes to Autopilot; it starts in shadow.
export const TERMINAL_CONSENT_FRAMES = partConsent.frames + 8;
// Autopilot has turned something on by itself.
export const TERMINAL_STATUS_FRAMES = partStatus.frames + 8;

const Scene: React.FC<{ part: Part; frames: number; toast?: boolean }> = ({
  part,
  frames,
  toast = false,
}) => {
  const frame = useCurrentFrame();
  const { portrait, terminal, caption } = useStage();
  const zoom = interpolate(frame, [0, frames], [1, 1.03], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  const toastW = portrait ? 900 : 760;
  return (
    <AbsoluteFill style={{ background: colors.bg }}>
      <div style={{ position: "absolute", left: terminal.x, top: terminal.y }}>
        <Terminal
          lines={part.lines}
          width={terminal.w}
          height={terminal.h}
          fontSize={terminal.font}
          title="zsh"
          zoom={zoom}
        />
      </div>
      {toast ? (
        <Toast
          {...TOAST}
          width={toastW}
          x={portrait ? (1080 - toastW) / 2 : 1920 - toastW - 48}
          y={portrait ? 12 : 28}
        />
      ) : null}
      <Captions items={part.captions} rect={caption} />
    </AbsoluteFill>
  );
};

export const InstallScene: React.FC = () => (
  <Scene part={partInstall} frames={TERMINAL_INSTALL_FRAMES} />
);

export const ConsentScene: React.FC = () => (
  <Scene part={partConsent} frames={TERMINAL_CONSENT_FRAMES} />
);

export const StatusTerminalScene: React.FC = () => (
  <Scene part={partStatus} frames={TERMINAL_STATUS_FRAMES} toast />
);
