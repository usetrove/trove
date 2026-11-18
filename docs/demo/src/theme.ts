import { loadFont as loadFigtree } from "@remotion/google-fonts/Figtree";
import { loadFont as loadBricolage } from "@remotion/google-fonts/BricolageGrotesque";
import { loadFont as loadMono } from "@remotion/google-fonts/JetBrainsMono";

// Colours come from the dark share card (internal/report/share) and the
// README banner (docs/assets/trove-banner.svg).
export const colors = {
  bg: "#0E1217",
  panel: "#171D25",
  rule: "#2A323D",
  text: "#F3F5F7",
  muted: "#9AA6B5",
  green: "#46D39A",
  amber: "#F2A83B",
  red: "#F0716E",
  paper1: "#fbfaf7",
  paper2: "#efede8",
  stroke: "#dcd9d2",
  ink: "#2d2e2a",
  inkSoft: "#6b6c68",
  // The brand accent: trove, as in treasure. Gold on dark, ochre on paper.
  // Status colours (green, amber, red) are never used for branding.
  gold: "#E0B04A",
  goldStrong: "#D4A137",
  inkGold: "#B8860B",
  inkGoldText: "#9A6A00",
  // Deprecated: the old accent; status green is `green`.
  inkGreen: "#15895b",
};

export const fonts = {
  // Wordmark typeface of the banner.
  display: loadFigtree("normal", { weights: ["700", "800"], subsets: ["latin"] })
    .fontFamily,
  // Tagline typeface of the banner; used for captions.
  body: loadBricolage("normal", { weights: ["500", "700"], subsets: ["latin"] })
    .fontFamily,
  mono: loadMono("normal", { weights: ["400", "700"], subsets: ["latin"] })
    .fontFamily,
};

// JetBrains Mono advances 600/1000 em per glyph. The terminal uses this for
// its column count and for placing highlight boxes and labels in pixels.
export const MONO_ADVANCE = 0.6;

export const FPS = 30;
