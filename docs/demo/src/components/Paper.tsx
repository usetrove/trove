import React from "react";
import { AbsoluteFill } from "remotion";
import { colors } from "../theme";

// The light card background of the README banner.
export const Paper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <AbsoluteFill
    style={{
      background: `linear-gradient(135deg, ${colors.paper1} 0%, ${colors.paper2} 100%)`,
      display: "flex",
      alignItems: "center",
      justifyContent: "center",
    }}
  >
    {children}
  </AbsoluteFill>
);
