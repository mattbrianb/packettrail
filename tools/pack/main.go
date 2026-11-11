// pack writes a .tar.gz release archive with proper Unix permissions, which
// Windows' tar cannot set. Files listed with -exec are marked executable
// (0755), so Linux users don't need chmod +x after extracting.
//
//	go run ./tools/pack -o dist/release/packettrail-linux-x64.tar.gz -prefix packettrail-linux-x64 \
//	    -exec dist/linux-amd64/packettrail README.md LICENSE THIRD-PARTY-NOTICES.md
package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	out := flag.String("o", "", "output .tar.gz")
	prefix := flag.String("prefix", "", "folder name inside the archive")
	var execs list
	flag.Var(&execs, "exec", "file to include as executable (repeatable)")
	flag.Parse()
	if *out == "" || *prefix == "" {
		fmt.Fprintln(os.Stderr, "usage: pack -o out.tar.gz -prefix name [-exec file]... file...")
		os.Exit(2)
	}
	if err := write(*out, *prefix, execs, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
		os.Exit(1)
	}
}

func write(out, prefix string, execs, files []string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	now := time.Now().Truncate(time.Second)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: prefix + "/", Mode: 0o755, ModTime: now}); err != nil {
		return err
	}
	add := func(src string, mode int64) error {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		h := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     path.Join(prefix, filepath.Base(src)),
			Mode:     mode,
			Size:     int64(len(data)),
			ModTime:  now,
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	}
	for _, e := range execs {
		if err := add(e, 0o755); err != nil {
			return err
		}
	}
	for _, p := range files {
		if err := add(p, 0o644); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}
