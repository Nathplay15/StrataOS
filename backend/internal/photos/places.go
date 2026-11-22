package photos

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"math"
	"strconv"
	"strings"
	"sync"
)

// places.tsv.gz: the towns of more than 15,000 people of GeoNames
// (geonames.org, CC BY 4.0), made by gen_places.go: name, country code,
// region, latitude, longitude, population. Reverse geocoding stays on the NAS.
//
//go:embed places.tsv.gz
var placesData []byte

// Place is a town.
type Place struct {
	ID      int     `json:"id"`
	City    string  `json:"city"`
	Region  string  `json:"region,omitempty"`
	Country string  `json:"country"` // ISO 3166 code: the interface names it in its language
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	pop     float64
}

// MaxPlaceKm: a picture farther than that from any town has no place.
const MaxPlaceKm = 60

var places struct {
	once sync.Once
	list []Place
	grid map[[2]int][]int // 1° cells → places
}

func loadPlaces() {
	z, err := gzip.NewReader(bytes.NewReader(placesData))
	if err != nil {
		return
	}
	places.grid = map[[2]int][]int{}
	sc := bufio.NewScanner(z)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 6 {
			continue
		}
		lat, e1 := strconv.ParseFloat(f[3], 64)
		lon, e2 := strconv.ParseFloat(f[4], 64)
		if e1 != nil || e2 != nil {
			continue
		}
		pop, _ := strconv.ParseFloat(f[5], 64)
		id := len(places.list)
		places.list = append(places.list, Place{ID: id, City: f[0], Country: f[1], Region: f[2], Lat: lat, Lon: lon, pop: max(pop, 15000)})
		c := cell(lat, lon)
		places.grid[c] = append(places.grid[c], id)
	}
}

func cell(lat, lon float64) [2]int { return [2]int{int(math.Floor(lat)), int(math.Floor(lon))} }

// distanceKm is the great-circle distance.
func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	r := math.Pi / 180
	dlat, dlon := (lat2-lat1)*r, (lon2-lon1)*r
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1*r)*math.Cos(lat2*r)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 6371 * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// Nearest returns the town of a position (within MaxPlaceKm): the closest,
// big towns counting as closer (a district of Paris is not the place of a
// picture taken in Paris).
func Nearest(lat, lon float64) (Place, bool) {
	places.once.Do(loadPlaces)
	best, bestD := -1, math.Inf(1)
	c := cell(lat, lon)
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			lo := c[1] + dx
			// Wrap around the antimeridian.
			if lo < -180 {
				lo += 360
			} else if lo >= 180 {
				lo -= 360
			}
			for _, id := range places.grid[[2]int{c[0] + dy, lo}] {
				p := places.list[id]
				d := distanceKm(lat, lon, p.Lat, p.Lon)
				if d > MaxPlaceKm {
					continue
				}
				if e := d / (1 + math.Log10(p.pop/15000)); e < bestD {
					best, bestD = id, e
				}
			}
		}
	}
	if best < 0 {
		return Place{}, false
	}
	return places.list[best], true
}

// PlaceByID returns a place of Nearest by its id.
func PlaceByID(id int) (Place, bool) {
	places.once.Do(loadPlaces)
	if id < 0 || id >= len(places.list) {
		return Place{}, false
	}
	return places.list[id], true
}
