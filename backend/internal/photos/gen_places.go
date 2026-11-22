//go:build ignore

// Generates places.tsv.gz (name, country, region, latitude, longitude, population) from the GeoNames dumps (CC BY 4.0,
// https://download.geonames.org/export/dump/): cities15000.txt (towns of
// more than 15,000 people, without the sections of towns) and admin1CodesASCII.txt. Usage:
//
//	go run gen_places.go cities15000.txt admin1CodesASCII.txt
package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	admin := map[string]string{}
	a, err := os.Open(os.Args[2])
	if err != nil {
		panic(err)
	}
	sc := bufio.NewScanner(a)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) > 1 {
			admin[f[0]] = f[1]
		}
	}
	c, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	out, _ := os.Create("places.tsv.gz")
	z, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	sc = bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 11 || f[7] == "PPLX" { // sections of a town (Paris 4e…)
			continue
		}
		lat, _ := strconv.ParseFloat(f[4], 64)
		lon, _ := strconv.ParseFloat(f[5], 64)
		pop, _ := strconv.Atoi(f[14])
		fmt.Fprintf(z, "%s\t%s\t%s\t%.3f\t%.3f\t%d\n", f[1], f[8], admin[f[8]+"."+f[10]], lat, lon, pop)
		n++
	}
	z.Close()
	out.Close()
	fmt.Println(n, "places")
}
