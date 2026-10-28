package gateway

import (
	"compress/gzip"
	"io"
)

func gzipWriter(w io.Writer) *gzip.Writer { return gzip.NewWriter(w) }
