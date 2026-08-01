package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/andyandymike/done-then/internal/sbom"
)

func main() {
	artifact := flag.String("artifact", "", "release archive to describe")
	binary := flag.String("binary", "", "DoneThen binary used to inventory Go modules")
	output := flag.String("output", "", "new SPDX JSON output path")
	version := flag.String("version", "", "release version without the v prefix")
	goos := flag.String("goos", "", "target operating system")
	goarch := flag.String("goarch", "", "target architecture")
	createdValue := flag.String("created", "", "reproducible RFC3339 creation time")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("unexpected positional arguments")
	}
	created, err := time.Parse(time.RFC3339, *createdValue)
	if err != nil {
		fatalf("parse --created: %v", err)
	}
	payload, err := sbom.Generate(sbom.Options{
		ArtifactPath: *artifact,
		BinaryPath:   *binary,
		Version:      *version,
		GOOS:         *goos,
		GOARCH:       *goarch,
		Created:      created,
	})
	if err != nil {
		fatalf("generate SPDX SBOM: %v", err)
	}
	if *output == "" {
		fatalf("--output is required")
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		fatalf("create output: %v", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(*output)
		fatalf("write output: %v", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(*output)
		fatalf("close output: %v", err)
	}
	fmt.Printf("Wrote SPDX 2.3 SBOM: %s\n", *output)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "donethen-sbom: "+format+"\n", args...)
	os.Exit(1)
}
