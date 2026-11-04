// This helper is a synthetic CLI, not Terraform. Never install it on a normal PATH.
package main

import (
	"fmt"
	"hclaro/fixtures"
	"os"
	"strings"
)

func main() {
	switch strings.Join(os.Args[1:], " ") {
	case "version -json":
		fmt.Println(`{"terraform_version":"1.9.8"}`)
	case "workspace show":
		fmt.Println("demo")
	case "show -json":
		_, _ = os.Stdout.Write(fixtures.State)
	default:
		fmt.Fprintln(os.Stderr, "synthetic CLI refuses this command")
		os.Exit(1)
	}
}
