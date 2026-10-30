// Command downloads prints GitHub release download counts for Trove, per release and per file.
// It reads public release data only; Trove itself sends nothing. These are downloads, not
// installs: CI jobs, mirrors and repeat downloads all count, and uninstalls cannot be counted.
// A private repo needs GITHUB_TOKEN (read access); the token is never printed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type asset struct {
	Name          string `json:"name"`
	DownloadCount int    `json:"download_count"`
}

type release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

func main() {
	repo := flag.String("repo", "usetrove/trove", "GitHub repository, owner/name")
	api := flag.String("api", "https://api.github.com", "GitHub API base URL (for tests)")
	asJSON := flag.Bool("json", false, "print JSON")
	flag.Parse()

	rels, err := fetch(*api, *repo, os.Getenv("GITHUB_TOKEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		os.Exit(1)
	}
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(summarize(rels))
		return
	}
	printReport(os.Stdout, rels)
}

func fetch(api, repo, token string) ([]release, error) {
	req, err := http.NewRequest("GET", strings.TrimRight(api, "/")+"/repos/"+repo+"/releases?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach GitHub: %v. Check your connection and try again", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("GitHub returned 404 for %s. The repo may be private or the name wrong; for a private repo set GITHUB_TOKEN to a token with read access", repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub returned HTTP %d. Wait a minute and retry, or set GITHUB_TOKEN if you hit the rate limit", resp.StatusCode)
	}
	var rels []release
	if err := json.Unmarshal(body, &rels); err != nil {
		return nil, fmt.Errorf("could not read GitHub's reply: %v", err)
	}
	return rels, nil
}

// countable reports whether an asset is a binary archive. Checksums, signatures and SBOMs
// are fetched by verification steps and would inflate the number.
func countable(name string) bool {
	return strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip")
}

type relSummary struct {
	Tag    string         `json:"tag"`
	Total  int            `json:"archive_downloads"`
	ByFile map[string]int `json:"by_file"`
}

func summarize(rels []release) []relSummary {
	out := []relSummary{}
	for _, r := range rels {
		if r.Draft {
			continue
		}
		s := relSummary{Tag: r.Tag, ByFile: map[string]int{}}
		for _, a := range r.Assets {
			if countable(a.Name) {
				s.ByFile[a.Name] = a.DownloadCount
				s.Total += a.DownloadCount
			}
		}
		out = append(out, s)
	}
	return out
}

func printReport(w io.Writer, rels []release) {
	sums := summarize(rels)
	if len(sums) == 0 {
		fmt.Fprintln(w, "No published releases yet, so there are no download counts.")
		return
	}
	grand := 0
	for _, s := range sums {
		grand += s.Total
		fmt.Fprintf(w, "%s: %d archive downloads\n", s.Tag, s.Total)
		names := make([]string, 0, len(s.ByFile))
		for n := range s.ByFile {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(w, "  %-50s %d\n", n, s.ByFile[n])
		}
	}
	fmt.Fprintf(w, "\nTotal: %d archive downloads (measured by GitHub).\n", grand)
	fmt.Fprintln(w, "These are downloads, not installs or users: CI, mirrors and repeat downloads count, and uninstalls cannot be counted.")
}
