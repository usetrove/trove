import React from "react";
import { linearTiming, TransitionSeries } from "@remotion/transitions";
import { fade } from "@remotion/transitions/fade";
import { HOOK_FRAMES, HookScene } from "./scenes/HookScene";
import { TERMINAL_FRAMES, TerminalScene } from "./scenes/TerminalScene";
import { STATUS_FRAMES, StatusScene } from "./scenes/StatusScene";
import { TITLE_FRAMES, TitleScene } from "./scenes/TitleScene";

export const FADE_FRAMES = 8;

// Scenes overlap by FADE_FRAMES at each of the three cuts.
export const DEMO_FRAMES =
  HOOK_FRAMES + TERMINAL_FRAMES + STATUS_FRAMES + TITLE_FRAMES - 3 * FADE_FRAMES;

const cut = () => (
  <TransitionSeries.Transition
    presentation={fade()}
    timing={linearTiming({ durationInFrames: FADE_FRAMES })}
  />
);

export const TroveDemo: React.FC = () => (
  <TransitionSeries>
    <TransitionSeries.Sequence name="Hook" durationInFrames={HOOK_FRAMES}>
      <HookScene />
    </TransitionSeries.Sequence>
    {cut()}
    <TransitionSeries.Sequence name="Terminal" durationInFrames={TERMINAL_FRAMES}>
      <TerminalScene />
    </TransitionSeries.Sequence>
    {cut()}
    <TransitionSeries.Sequence name="Status line" durationInFrames={STATUS_FRAMES}>
      <StatusScene />
    </TransitionSeries.Sequence>
    {cut()}
    <TransitionSeries.Sequence name="Title" durationInFrames={TITLE_FRAMES}>
      <TitleScene />
    </TransitionSeries.Sequence>
  </TransitionSeries>
);
