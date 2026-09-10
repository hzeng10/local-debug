package main

import (
	"devctl.local/devctl/internal/devctl"
	"os"
)

func main() { os.Exit(devctl.Main(os.Args[1:], os.Stdout, os.Stderr)) }
