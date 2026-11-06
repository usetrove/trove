package contribute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
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

// exchange makes the one request this package ever makes: bounded by Timeout, following no redirects, carrying no
// credentials or cookies, and reading at most a few bytes of the reply. Errors never include the reply or the URL,
// so nothing the server says can reach logs.
func exchange(ctx context.Context, method, url string, body []byte) (status int, err error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, fmt.Errorf("cannot build the request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "trove-contribute")
	client := &http.Client{
		Timeout:       Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New(shortErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.CopyN(io.Discard, resp.Body, 512)
	return resp.StatusCode, nil
}

// Send posts body to endpoint.
func Send(ctx context.Context, endpoint string, body []byte) error {
	if endpoint == "" {
		return fmt.Errorf("contribute.endpoint is empty, so there is nowhere to send the summary. Set it in config.toml")
	}
	if problem := config.ValidateEndpoint(endpoint); problem != "" {
		return fmt.Errorf("contribute.endpoint %s", problem)
	}
	status, err := exchange(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("could not reach the summary endpoint (%s). Nothing was sent; it will be tried again later", err)
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("the summary endpoint answered HTTP %d, so the summary was not accepted. It will be tried again later", status)
	}
	return nil
}

var installIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// DeleteURL is where a request to delete everything stored under installID goes: the contribution endpoint, a slash
// and the install id. The receiver (server/worker) answers 204 whether or not it held anything.
func DeleteURL(endpoint, installID string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("contribute.endpoint is empty, so there is nowhere to send the request. Set it in config.toml")
	}
	if problem := config.ValidateEndpoint(endpoint); problem != "" {
		return "", fmt.Errorf("contribute.endpoint %s", problem)
	}
	if !installIDPattern.MatchString(installID) {
		return "", errors.New("there is no valid install id on this computer, so there is nothing to ask the receiver to delete")
	}
	return strings.TrimRight(endpoint, "/") + "/" + installID, nil
}

// Delete asks the receiver to delete every summary stored under installID. It runs only when the user runs
// `trove contribute delete` (CLAUDE.md rule 1) and is never called by anything automatic. It sends no body, no
// credentials and no cookies, and follows no redirects.
func Delete(ctx context.Context, endpoint, installID string) error {
	url, err := DeleteURL(endpoint, installID)
	if err != nil {
		return err
	}
	status, err := exchange(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("could not reach the summary endpoint (%s). Nothing was deleted and your install id is kept, so you can try again", err)
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("the summary endpoint answered HTTP %d, so the request was not accepted. Nothing was deleted and your install id is kept, so you can try again", status)
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
