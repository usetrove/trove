package share

import (
	"fmt"
	"os"
	"path/filepath"
)

// Save writes base+".svg" and base+".png" (creating the folder if needed) and
// returns both paths. It writes nothing anywhere else and never uploads.
func Save(base string, c Card, th Theme) (svgPath, pngPath string, err error) {
	pngBytes, err := PNG(c, th)
	if err != nil {
		return "", "", fmt.Errorf("cannot draw the card: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil { //nolint:gosec // a folder the user chose; the card is meant to be shared
		return "", "", fmt.Errorf("cannot create %s: %v. Choose another place with --out", filepath.Dir(base), err)
	}
	svgPath, pngPath = base+".svg", base+".png"
	if err := os.WriteFile(svgPath, []byte(SVG(c, th)), 0o644); err != nil { //nolint:gosec // meant to be shared
		return "", "", fmt.Errorf("cannot write %s: %v. Choose another place with --out", svgPath, err)
	}
	if err := os.WriteFile(pngPath, pngBytes, 0o644); err != nil { //nolint:gosec // meant to be shared
		return svgPath, "", fmt.Errorf("cannot write %s: %v. Choose another place with --out", pngPath, err)
	}
	return svgPath, pngPath, nil
}
