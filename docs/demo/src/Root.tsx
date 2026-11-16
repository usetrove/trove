import React from "react";
import { Composition, Folder } from "remotion";
import { HOOK_FRAMES, HookScene } from "./scenes/HookScene";
import { TERMINAL_FRAMES, TerminalScene } from "./scenes/TerminalScene";
import { STATUS_FRAMES, StatusScene } from "./scenes/StatusScene";
import { TITLE_FRAMES, TitleScene } from "./scenes/TitleScene";
import { FPS } from "./theme";
import { DEMO_FRAMES, TroveDemo } from "./TroveDemo";

const landscape = { width: 1920, height: 1080, fps: FPS };
const portrait = { width: 1080, height: 1920, fps: FPS };

export const RemotionRoot: React.FC = () => (
  <>
    <Composition
      id="TroveDemo"
      component={TroveDemo}
      durationInFrames={DEMO_FRAMES}
      {...landscape}
    />
    <Composition
      id="TroveDemoVertical"
      component={TroveDemo}
      durationInFrames={DEMO_FRAMES}
      {...portrait}
    />
    <Folder name="Scenes">
      <Composition id="Hook" component={HookScene} durationInFrames={HOOK_FRAMES} {...landscape} />
      <Composition
        id="Terminal"
        component={TerminalScene}
        durationInFrames={TERMINAL_FRAMES}
        {...landscape}
      />
      <Composition
        id="StatusLine"
        component={StatusScene}
        durationInFrames={STATUS_FRAMES}
        {...landscape}
      />
      <Composition id="Title" component={TitleScene} durationInFrames={TITLE_FRAMES} {...landscape} />
      <Composition
        id="TerminalVertical"
        component={TerminalScene}
        durationInFrames={TERMINAL_FRAMES}
        {...portrait}
      />
    </Folder>
  </>
);
