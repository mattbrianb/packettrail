// Command geoipseed prepares the validated offline databases for a release.
package main

import (
	"flag"
	"fmt"
	"os"

	"packettrail/internal/enrich"
)

func main() {
	dir := flag.String("dir", "dist/data", "directory for DB-IP Lite databases")
	flag.Parse()
	e := enrich.New(*dir, nil)
	defer e.Close()
	if !e.Missing() {
		fmt.Println("GeoIP installer seed already present:", *dir)
		return
	}
	if err := e.Download(); err != nil {
		fmt.Fprintln(os.Stderr, "GeoIP installer seed:", err)
		os.Exit(1)
	}
	if e.Missing() {
		fmt.Fprintln(os.Stderr, "GeoIP installer seed is still incomplete")
		os.Exit(1)
	}
	fmt.Println("GeoIP installer seed ready:", *dir)
}
