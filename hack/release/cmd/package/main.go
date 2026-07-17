package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	release "github.com/higress-group/issue-spec/hack/release"
)

func main() {
	var options release.Options
	var verify string
	flag.StringVar(&options.Root, "root", ".", "repository root")
	flag.StringVar(&options.Output, "out", "dist/release", "release output directory")
	flag.StringVar(&options.Ref, "ref", "", "trusted Git ref")
	flag.StringVar(&options.Revision, "revision", "", "exact full source revision")
	flag.Int64Var(&options.SourceDateEpoch, "source-date-epoch", 0, "reproducible source timestamp")
	flag.StringVar(&verify, "verify", "", "verify an already assembled release directory")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(fmt.Errorf("unexpected positional arguments: %v", flag.Args()))
	}
	if verify != "" {
		manifest, err := release.VerifyDirectory(verify)
		if err != nil {
			fatal(err)
		}
		writeJSON(manifest)
		return
	}
	plan, err := release.Package(context.Background(), options)
	if err != nil {
		fatal(err)
	}
	writeJSON(plan)
}

func writeJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "release:", err)
	os.Exit(1)
}
