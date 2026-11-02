package contribute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// Timeout bounds one upload. It is short on purpose: upload is a side errand and
// must never hold up the command the user ran.
const Timeout = 4 * time.Second

// Encode is the exact text shown by preview, written by --export and sent by
// upload. One function means the three cannot differ.
func Encode(p Payload) ([]byte, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Send posts body to endpoint. It follows no redirects, sends no credentials or
// cookies, and reads at most a few bytes of the reply. Errors never include the
// reply, so nothing the server says can reach logs.
func Send(ctx context.Context, endpoint string, body []byte) error {
	if endpoint == "" {
		return fmt.Errorf("contribute.endpoint is empty, so there is nowhere to send the summary. Set it in config.toml")
	}
	if problem := config.ValidateEndpoint(endpoint); problem != "" {
		return fmt.Errorf("contribute.endpoint %s", problem)
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot build the upload request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "trove-contribute")
	client := &http.Client{
		Timeout:       Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the summary endpoint (%s). Nothing was sent; it will be tried again later", shortErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.CopyN(io.Discard, resp.Body, 512)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the summary endpoint answered HTTP %d, so the summary was not accepted. It will be tried again later", resp.StatusCode)
	}
	return nil
}

// shortErr keeps the error class and drops the URL that net/http puts in it.
func shortErr(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "network error"
}

// Auto sends the payload when consent is on and an upload is due. It returns
// whether it sent. Every failure leaves the state untouched so the next run
// tries again; callers show nothing for it.
func Auto(ctx context.Context, dir, endpoint string, p Payload, now time.Time) (bool, error) {
	st, err := LoadState(dir)
	if err != nil || !st.Due(now) || endpoint == "" {
		return false, err
	}
	p.InstallID = st.InstallID
	body, err := Encode(p)
	if err != nil {
		return false, err
	}
	if err := Send(ctx, endpoint, body); err != nil {
		return false, err
	}
	st.LastSent = now.UTC().Format(time.DateOnly)
	return true, SaveState(dir, st)
}
