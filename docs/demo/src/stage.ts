import { useVideoConfig } from "remotion";

export type Rect = { x: number; y: number; w: number; h: number };

export type Stage = {
  portrait: boolean;
  terminal: Rect & { font: number };
  caption: Rect & { font: number };
};

// One geometry for each orientation. Every scene lays itself out from this,
// so the landscape and the vertical video share all other code.
export const useStage = (): Stage => {
  const { width, height } = useVideoConfig();
  if (height > width) {
    return {
      portrait: true,
      terminal: { x: 48, y: 150, w: 984, h: 1270, font: 24 },
      caption: { x: 90, y: 1490, w: 900, h: 300, font: 50 },
    };
  }
  return {
    portrait: false,
    terminal: { x: 80, y: 64, w: 1760, h: 800, font: 26 },
    caption: { x: 160, y: 884, w: 1600, h: 120, font: 50 },
  };
};
