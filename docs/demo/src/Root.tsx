import React from "react";
import { Composition, Folder } from "remotion";
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
import { FPS } from "./theme";
import { DEMO_FRAMES, TroveDemo } from "./TroveDemo";

const landscape = { width: 1920, height: 1080, fps: FPS };
const portrait = { width: 1080, height: 1920, fps: FPS };

const scenes: [string, React.FC, number][] = [
  ["Hook", HookScene, HOOK_FRAMES],
  ["Install", InstallScene, TERMINAL_INSTALL_FRAMES],
  ["Report", ReportScene, REPORT_FRAMES],
  ["Consent", ConsentScene, TERMINAL_CONSENT_FRAMES],
  ["TimeSkip", TimeSkipScene, TIMESKIP_FRAMES],
  ["Status", StatusTerminalScene, TERMINAL_STATUS_FRAMES],
  ["Lifecycle", LifecycleScene, LIFECYCLE_FRAMES],
  ["StatusLine", StatusScene, STATUS_FRAMES],
  ["Title", TitleScene, TITLE_FRAMES],
];

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
      {scenes.map(([id, component, frames]) => (
        <Composition key={id} id={id} component={component} durationInFrames={frames} {...landscape} />
      ))}
    </Folder>
    <Folder name="ScenesVertical">
      {scenes.map(([id, component, frames]) => (
        <Composition
          key={id}
          id={`${id}Vertical`}
          component={component}
          durationInFrames={frames}
          {...portrait}
        />
      ))}
    </Folder>
  </>
);
