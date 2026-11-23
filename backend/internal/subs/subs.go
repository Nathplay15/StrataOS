// Package subs fetches subtitle files from OpenSubtitles (REST API v1) for
// a video: login with the user's own credentials (stored masked), search by
// IMDb id (from TMDB) or by the file's moviehash, download the best match in
// the preferred languages. The endpoints are variables so tests can point
// them at a local server.
package subs

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	Base      = "https://api.opensubtitles.com/api/v1"
	HTTP      = &http.Client{Timeout: 20 * time.Second}
	UserAgent = "StrataOS v0"
)

// Client talks to OpenSubtitles with the user's API key and account.
type Client struct {
	APIKey   string
	User     string
	Password string
	token    string
	tokenExp time.Time
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any, bearer bool) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, Base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Api-Key", c.APIKey)
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("opensubtitles: HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("opensubtitles: invalid answer")
		}
	}
	return nil
}

// Login exchanges the credentials for a JWT (cached until it expires).
func (c *Client) Login(ctx context.Context) error {
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return nil
	}
	if c.APIKey == "" || c.User == "" || c.Password == "" {
		return fmt.Errorf("opensubtitles credentials are not configured")
	}
	var res struct {
		Token  string `json:"token"`
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodPost, "/login", map[string]string{
		"username": c.User, "password": c.Password,
	}, &res, false); err != nil {
		return err
	}
	if res.Token == "" {
		return fmt.Errorf("opensubtitles refused the login")
	}
	c.token = res.Token
	c.tokenExp = time.Now().Add(23 * time.Hour) // the API tokens last 24 h
	return nil
}

// SearchResult is one subtitle file found for a video.
type SearchResult struct {
	FileID          int
	Lang            string
	Name            string
	Rating          float64
	Downloads       int
	HearingImpaired bool
}

// Search looks subtitles up by IMDb id or by moviehash, in the preferred
// languages (comma separated, e.g. "fr,en").
func (c *Client) Search(ctx context.Context, imdbID, moviehash, langs string) ([]SearchResult, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	q := url.Values{"languages": {langs}}
	if imdbID != "" {
		q.Set("imdb_id", strings.TrimPrefix(imdbID, "tt"))
	} else if moviehash != "" {
		q.Set("moviehash", moviehash)
	} else {
		return nil, fmt.Errorf("no imdb id nor moviehash")
	}
	var res struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Language string `json:"language"`
				Files    []struct {
					FileID   int    `json:"file_id"`
					FileName string `json:"file_name"`
				} `json:"files"`
				Release        string  `json:"release"`
				Rating         float64 `json:"rating"`
				DownloadCount  int     `json:"download_count"`
				FeatureDetails struct {
					HearingImpaired bool `json:"hearing_impaired"`
				} `json:"feature_details"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/subtitles?"+q.Encode(), nil, &res, true); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, d := range res.Data {
		for _, f := range d.Attributes.Files {
			if f.FileID == 0 {
				continue
			}
			out = append(out, SearchResult{
				FileID: f.FileID, Lang: d.Attributes.Language, Name: f.FileName,
				Rating: d.Attributes.Rating, Downloads: d.Attributes.DownloadCount,
				HearingImpaired: d.Attributes.FeatureDetails.HearingImpaired,
			})
		}
	}
	return out, nil
}

// Download fetches the content of one subtitle file (SubRip).
func (c *Client) Download(ctx context.Context, fileID int) ([]byte, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	var res struct {
		Link string `json:"link"`
	}
	body, _ := json.Marshal(map[string]any{"file_id": fileID})
	if err := c.do(ctx, http.MethodPost, "/download", json.RawMessage(body), &res, true); err != nil {
		return nil, err
	}
	if res.Link == "" {
		return nil, fmt.Errorf("opensubtitles returned no download link")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, res.Link, nil)
	if err != nil {
		return nil, err
	}
	resp, err := HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// MovieHash computes the OpenSubtitles moviehash of a file: MD5 over the
// first 64 KiB, the last 64 KiB and the file size (little endian).
func MovieHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	h := md5.New()
	buf := make([]byte, 64<<10)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	h.Write(buf[:n])
	if fi.Size() > int64(len(buf)) {
		if _, err := f.Seek(max(0, fi.Size()-int64(len(buf))), io.SeekStart); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return "", err
		}
		h.Write(buf[:n])
	}
	var size [8]byte
	for i := 0; i < 8; i++ {
		size[i] = byte(fi.Size() >> (8 * i))
	}
	h.Write(size[:])
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SanitizeLang keeps only [a-z0-9-] (5 chars max) so a language coming from
// the API can be used in a file name without any path component; "und" when
// nothing usable remains.
func SanitizeLang(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 5 {
			break
		}
	}
	if b.Len() == 0 {
		return "und"
	}
	return b.String()
}

// LangOf extracts the language of a subtitle sidecar name ("movie.fr.srt").
func LangOf(name string) string {
	parts := strings.Split(strings.TrimSuffix(name, filepath.Ext(name)), ".")
	if len(parts) >= 2 {
		l := parts[len(parts)-1]
		if len(l) >= 2 && len(l) <= 5 {
			return l
		}
	}
	return ""
}

// ParseLangs normalizes the preferred languages setting ("fr, en" -> "fr,en").
func ParseLangs(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	return strings.Join(parts, ",")
}

// PickBest returns the index of the best result: preferred languages first
// (in the order given), then non-hearing-impaired, then rating, then
// downloads. -1 when the list is empty.
func PickBest(results []SearchResult, langs string) int {
	order := strings.Split(langs, ",")
	best := -1
	for i := range results {
		if best < 0 || subScore(results[i], order) > subScore(results[best], order) {
			best = i
		}
	}
	return best
}

func subScore(r SearchResult, order []string) int {
	s := 0
	for i, l := range order {
		if r.Lang == l {
			s += (len(order) - i) * 1000
			break
		}
	}
	if !r.HearingImpaired {
		s += 500
	}
	s += int(r.Rating) * 10
	s += min(r.Downloads, 100000) / 100
	return s
}
