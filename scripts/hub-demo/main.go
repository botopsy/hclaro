// hub-demo writes the synthetic Workspace Hub projects (fixtures.HubProjects) below a
// directory and registers them in a registry there, for `make demo-hub` and browser checks.
// It never touches the real per-user registry.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/projects"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: hub-demo <empty directory>")
		os.Exit(2)
	}
	dir, err := filepath.Abs(os.Args[1])
	if err == nil {
		err = fixtures.WriteHubProjects(filepath.Join(dir, "projects"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hub-demo:", err)
		os.Exit(1)
	}
	hub := projects.NewHub(projects.Open(filepath.Join(dir, "config"), time.Now()), "")
	defer hub.Close()
	for _, p := range fixtures.HubProjects {
		if _, err := hub.Add(filepath.Join(dir, "projects", p.Dir), "", p.Engine); err != nil {
			fmt.Fprintln(os.Stderr, "hub-demo:", err)
			os.Exit(1)
		}
	}
	fmt.Println(filepath.Join(dir, "config"))
}
