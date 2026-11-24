package cinema

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TMDB endpoints; variables so tests can point them at a local server.
var (
	TMDBBase   = "https://api.themoviedb.org/3"
	TMDBImages = "https://image.tmdb.org/t/p/w500"
)

// Client talks to TMDB (the user's own free API key).
type Client struct {
	Key  string
	HTTP *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// SearchResult is one hit of a title search.
type SearchResult struct {
	ID       int     `json:"id"`
	Title    string  `json:"title"` // movie: title, series: name
	Year     int     `json:"year,omitempty"`
	Overview string  `json:"overview,omitempty"`
	Rating   float64 `json:"rating,omitempty"`
	Poster   string  `json:"poster,omitempty"` // poster file name (image path)
}

// Search looks a title up on TMDB: kind is "movie" or "tv".
func (c *Client) Search(ctx context.Context, kind, title string, year int) ([]SearchResult, error) {
	if c.Key == "" {
		return nil, fmt.Errorf("no TMDB API key")
	}
	q := url.Values{"api_key": {c.Key}, "query": {title}, "include_adult": {"false"}}
	if year > 0 {
		if kind == "tv" {
			q.Set("first_air_date_year", strconv.Itoa(year))
		} else {
			q.Set("year", strconv.Itoa(year))
		}
	}
	u := TMDBBase + "/search/" + kind + "?" + q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Results []struct {
			ID           int     `json:"id"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			Overview     string  `json:"overview"`
			VoteAverage  float64 `json:"vote_average"`
			PosterPath   string  `json:"poster_path"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(body.Results))
	for _, r := range body.Results {
		date := r.ReleaseDate
		if date == "" {
			date = r.FirstAirDate
		}
		yr := 0
		if len(date) >= 4 {
			yr, _ = strconv.Atoi(date[:4])
		}
		t := r.Title
		if t == "" {
			t = r.Name
		}
		out = append(out, SearchResult{ID: r.ID, Title: t, Year: yr, Overview: r.Overview, Rating: r.VoteAverage, Poster: strings.TrimPrefix(r.PosterPath, "/")})
	}
	return out, nil
}

// Details fetches the full record of one TMDB id and fills the library fields.
func (c *Client) Details(ctx context.Context, kind string, id int) (*Item, error) {
	if c.Key == "" {
		return nil, fmt.Errorf("no TMDB API key")
	}
	u := fmt.Sprintf("%s/%s/%d?api_key=%s&language=en&append_to_response=credits,external_ids,release_dates,content_ratings", TMDBBase, map[string]string{"movie": "movie", "tv": "tv"}[kind], id, url.QueryEscape(c.Key))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Title        string  `json:"title"`
		Name         string  `json:"name"`
		ReleaseDate  string  `json:"release_date"`
		FirstAirDate string  `json:"first_air_date"`
		Overview     string  `json:"overview"`
		VoteAverage  float64 `json:"vote_average"`
		PosterPath   string  `json:"poster_path"`
		ExternalIDs  struct {
			IMDBID string `json:"imdb_id"`
		} `json:"external_ids"`
		Collection *struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"belongs_to_collection"`
		Runtime        int   `json:"runtime"`
		EpisodeRunTime []int `json:"episode_run_time"`
		Genres         []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Credits struct {
			Cast []struct {
				Name string `json:"name"`
			} `json:"cast"`
		} `json:"credits"`
		ReleaseDates struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Dates   []struct {
					Cert string `json:"certification"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
		ContentRatings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, err
	}
	it := &Item{TMDB: id, Overview: body.Overview, Rating: body.VoteAverage, Poster: strings.TrimPrefix(body.PosterPath, "/")}
	if body.Collection != nil {
		it.Collection, it.CollectionID = body.Collection.Name, body.Collection.ID
	}
	it.IMDB = body.ExternalIDs.IMDBID
	if body.Title != "" {
		it.Title = body.Title
		it.Kind = "movie"
		if len(body.ReleaseDate) >= 4 {
			it.Year, _ = strconv.Atoi(body.ReleaseDate[:4])
		}
		it.Runtime = body.Runtime
	} else {
		it.Title = body.Name
		it.Kind = "episode"
		if len(body.FirstAirDate) >= 4 {
			it.Year, _ = strconv.Atoi(body.FirstAirDate[:4])
		}
		if len(body.EpisodeRunTime) > 0 {
			it.Runtime = body.EpisodeRunTime[0]
		}
	}
	certs := map[string]string{}
	for _, r := range body.ReleaseDates.Results {
		for _, d := range r.Dates {
			if d.Cert != "" && certs[r.Country] == "" {
				certs[r.Country] = d.Cert
			}
		}
	}
	for _, r := range body.ContentRatings.Results {
		if r.Rating != "" && certs[r.Country] == "" {
			certs[r.Country] = r.Rating
		}
	}
	it.Age, it.Rated = AgeOf(certs)
	for _, g := range body.Genres {
		it.Genres = append(it.Genres, g.Name)
	}
	for i, a := range body.Credits.Cast {
		if i >= 5 {
			break
		}
		it.Cast = append(it.Cast, a.Name)
	}
	return it, nil
}

// Video is one video (trailer, teaser…) of a TMDB record.
type Video struct {
	Key      string `json:"key"`  // e.g. the YouTube key
	Site     string `json:"site"` // "YouTube"…
	Type     string `json:"type"` // "Trailer", "Teaser"…
	Name     string `json:"name"`
	Official bool   `json:"official"`
}

// Trailer returns the best YouTube video of a TMDB record: official trailers
// first, then any trailer, then anything else on YouTube. Key is "" when the
// record has no YouTube video. kind is "movie" or "tv".
func (c *Client) Trailer(ctx context.Context, kind string, id int) (Video, error) {
	if c.Key == "" {
		return Video{}, fmt.Errorf("no TMDB API key")
	}
	if kind != "movie" && kind != "tv" {
		return Video{}, fmt.Errorf("invalid kind %q", kind)
	}
	u := fmt.Sprintf("%s/%s/%d/videos?api_key=%s&language=en", TMDBBase, kind, id, url.QueryEscape(c.Key))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http().Do(req)
	if err != nil {
		return Video{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Video{}, fmt.Errorf("TMDB: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Results []Video `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return Video{}, err
	}
	best, bestScore := Video{}, -1
	for _, v := range body.Results {
		if !strings.EqualFold(v.Site, "YouTube") || v.Key == "" {
			continue
		}
		score := 0
		if strings.EqualFold(v.Type, "Trailer") {
			score += 2
		}
		if v.Official {
			score++
		}
		if score > bestScore {
			best, bestScore = v, score
		}
	}
	return best, nil
}

// Poster fetches a poster image (posterFile is the TMDB path, without the
// leading slash) and returns its bytes.
func (c *Client) Poster(ctx context.Context, posterFile string) ([]byte, error) {
	if posterFile == "" {
		return nil, fmt.Errorf("no poster")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, TMDBImages+"/"+posterFile, nil)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB image: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// certCountries is the order in which the certifications are read.
var certCountries = []string{"FR", "US", "GB", "DE", "ES", "IT", "NL", "BR", "PT", "CA", "AU"}

// namedCerts maps the non-numeric certifications to a minimum age.
var namedCerts = map[string]int{
	// US films and TV
	"G": 0, "PG": 10, "PG-13": 13, "R": 17, "NC-17": 18,
	"TV-Y": 0, "TV-Y7": 7, "TV-Y7-FV": 7, "TV-G": 0, "TV-PG": 10, "TV-14": 14, "TV-MA": 17,
	// UK
	"U": 0, "UC": 0, "12A": 12, "R18": 18,
	// France, Brazil, Portugal, Netherlands, Spain, Italy
	"TP": 0, "TOUS PUBLICS": 0, "L": 0, "AL": 0, "A": 0, "T": 0, "E": 0,
	"M/3": 3, "M/6": 6, "M/12": 12, "M/14": 14, "M/16": 16, "M/18": 18,
	"VM14": 14, "VM18": 18, "APTA": 0, "ERI": 0,
	// Canada, Australia
	"14A": 14, "18A": 18, "M": 15, "MA15+": 15, "R18+": 18, "X18+": 18,
}

// AgeOf returns the minimum age of a work from its certifications by
// country (TMDB release_dates / content_ratings), read in certCountries
// order; rated is false when none is known.
func AgeOf(certs map[string]string) (age int, rated bool) {
	for _, c := range certCountries {
		if a, ok := certAge(certs[c]); ok {
			return a, true
		}
	}
	return 0, false
}

func certAge(c string) (int, bool) {
	c = strings.ToUpper(strings.TrimSpace(c))
	if c == "" {
		return 0, false
	}
	if a, ok := namedCerts[c]; ok {
		return a, true
	}
	// "12", "16", "FSK 12", "-12", "12+", "AL 12"…
	digits := strings.TrimFunc(c, func(r rune) bool { return r < '0' || r > '9' })
	if a, err := strconv.Atoi(digits); err == nil && a >= 0 && a <= 21 {
		return a, true
	}
	return 0, false
}
