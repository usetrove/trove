import React from "react";
import { linearTiming, TransitionSeries } from "@remotion/transitions";
import { fade } from "@remotion/transitions/fade";
import { slide } from "@remotion/transitions/slide";
import { AbsoluteFill, Audio, interpolate, staticFile } from "remotion";
import { HOOK_FRAMES, HookScene } from "./scenes/HookScene";
import { LIFECYCLE_FRAMES, LifecycleScene } from "./scenes/LifecycleScene";
import { REPORT_FRAMES, ReportScene } from "./scenes/ReportScene";
import { STATUS_FRAMES, StatusScene } from "./scenes/StatusScene";
import {
  ConsentScene,
  InstallScene,
  StatusTerminalScene,
  TERMINAL_CONSENT_FRAMES,
  TERMINAL_INSTALL_FRAMES,
  TERMINAL_STATUS_FRAMES,
} from "./scenes/TerminalScene";
import { TIMESKIP_FRAMES, TimeSkipScene } from "./scenes/TimeSkipScene";
import { TITLE_FRAMES, TitleScene } from "./scenes/TitleScene";

export const FADE_FRAMES = 10;
export const SLIDE_FRAMES = 14;

// Scenes overlap by the transition length at each cut: six fades, two slides.
export const DEMO_FRAMES =
  HOOK_FRAMES +
  TERMINAL_INSTALL_FRAMES +
  REPORT_FRAMES +
  TERMINAL_CONSENT_FRAMES +
  TIMESKIP_FRAMES +
  TERMINAL_STATUS_FRAMES +
  LIFECYCLE_FRAMES +
  STATUS_FRAMES +
  TITLE_FRAMES -
  6 * FADE_FRAMES -
  2 * SLIDE_FRAMES;

// "Calm" by Sub Clair, supplied by the project owner (see public/music/LICENSE.txt
// for the source and credit to confirm before publishing): a slow lo-fi groove
// around 74 bpm, even loudness, a one-second fade-in that the hook's own fade
// covers. Measured -10.5 LUFS over the cut, so it plays at a low level to sit
// under the captions. Fades in with the hook and out with the title card.
// See README, Music.
export const MUSIC = {
  file: "music/sub-clair-calm.mp3",
  level: 0.3,
};

const cut = () => (
  <TransitionSeries.Transition
    presentation={fade()}
    timing={linearTiming({ durationInFrames: FADE_FRAMES })}
  />
);

const push = () => (
  <TransitionSeries.Transition
    presentation={slide({ direction: "from-bottom" })}
    timing={linearTiming({ durationInFrames: SLIDE_FRAMES })}
  />
);

export const TroveDemo: React.FC = () => (
  <AbsoluteFill>
    <Audio
      src={staticFile(MUSIC.file)}
      volume={(f) =>
        interpolate(
          f,
          [0, 30, DEMO_FRAMES - 70, DEMO_FRAMES - 6],
          [0, MUSIC.level, MUSIC.level, 0],
          { extrapolateLeft: "clamp", extrapolateRight: "clamp" },
        )
      }
    />
    <TransitionSeries>
      <TransitionSeries.Sequence name="Hook" durationInFrames={HOOK_FRAMES}>
        <HookScene />
      </TransitionSeries.Sequence>
      {cut()}
      <TransitionSeries.Sequence name="Install" durationInFrames={TERMINAL_INSTALL_FRAMES}>
        <InstallScene />
      </TransitionSeries.Sequence>
      {cut()}
      <TransitionSeries.Sequence name="Report" durationInFrames={REPORT_FRAMES}>
        <ReportScene />
      </TransitionSeries.Sequence>
      {cut()}
      <TransitionSeries.Sequence name="Consent" durationInFrames={TERMINAL_CONSENT_FRAMES}>
        <ConsentScene />
      </TransitionSeries.Sequence>
      {push()}
      <TransitionSeries.Sequence name="Time skip" durationInFrames={TIMESKIP_FRAMES}>
        <TimeSkipScene />
      </TransitionSeries.Sequence>
      {push()}
      <TransitionSeries.Sequence name="Status" durationInFrames={TERMINAL_STATUS_FRAMES}>
        <StatusTerminalScene />
      </TransitionSeries.Sequence>
      {cut()}
      <TransitionSeries.Sequence name="Lifecycle" durationInFrames={LIFECYCLE_FRAMES}>
        <LifecycleScene />
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
  </AbsoluteFill>
);
