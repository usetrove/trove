import React from "react";
import { AbsoluteFill } from "remotion";
import { Captions } from "../components/Caption";
import { Terminal } from "../components/Terminal";
import { SESSION_FRAMES, sessionCaptions, sessionLines } from "../script";
import { useStage } from "../stage";
import { colors } from "../theme";

// Install, report, fix and apply happen in one continuous terminal session.
export const TERMINAL_FRAMES = SESSION_FRAMES + 8;

export const TerminalScene: React.FC = () => {
  const { terminal, caption } = useStage();
  return (
    <AbsoluteFill style={{ background: colors.bg }}>
      <div style={{ position: "absolute", left: terminal.x, top: terminal.y }}>
        <Terminal
          lines={sessionLines}
          width={terminal.w}
          height={terminal.h}
          fontSize={terminal.font}
          title="zsh"
        />
      </div>
      <Captions items={sessionCaptions} rect={caption} />
    </AbsoluteFill>
  );
};
