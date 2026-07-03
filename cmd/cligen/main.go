// Command cligen turns an enriched OpenAPI v3 spec into a cobra "domain command
// module" that imports the clikit runtime. It is the CLI-side analog of the apx
// "go" client generator; de cli add (and a future apx "cli" generator) run it.
//
// Usage:
//
//	cligen --input <spec> --output <dir> --module <path> --domain <name> \
//	       --package <pkg> --app <appName>
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/infobloxopen/devedge-cli-sdk/internal/gen"
)

func main() {
	var (
		input   = flag.String("input", "", "path to the enriched OpenAPI v3 spec (required)")
		output  = flag.String("output", "", "output directory for the generated package (required)")
		module  = flag.String("module", "", "go module path (for a standalone go.mod / imports)")
		domain  = flag.String("domain", "", "domain command name (required)")
		pkg     = flag.String("package", "", "generated Go package name (defaults to --domain)")
		appName = flag.String("app", "ib", "rebranded application name")
	)
	flag.Parse()

	if err := run(*input, *output, *module, *domain, *pkg, *appName); err != nil {
		fmt.Fprintln(os.Stderr, "cligen:", err)
		os.Exit(1)
	}
}

func run(input, output, module, domain, pkg, appName string) error {
	// Preflight: fail loud on missing inputs before touching the filesystem.
	if input == "" {
		return fmt.Errorf("--input is required")
	}
	if output == "" {
		return fmt.Errorf("--output is required")
	}
	if domain == "" {
		return fmt.Errorf("--domain is required")
	}
	if _, err := os.Stat(input); err != nil {
		return fmt.Errorf("--input spec not readable: %w", err)
	}
	if pkg == "" {
		pkg = domain
	}
	if module == "" {
		module = "example.com/" + appName + "-" + domain
	}

	written, err := gen.Generate(gen.Options{
		SpecPath:  input,
		OutputDir: output,
		Module:    module,
		Domain:    domain,
		Package:   pkg,
		App:       appName,
	})
	if err != nil {
		return err
	}
	for _, f := range written {
		fmt.Println("wrote", f)
	}
	return nil
}
