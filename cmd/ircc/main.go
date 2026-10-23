package main

import (
	"os"
)

func main() { os.Exit(run(os.Args[1:], nil)) }

func run(args []string, ready func()) int {
	if len(args) == 0 {
		return runTUI()
	}
	if args[0] == "keep" {
		return runKeep(ready)
	}
	os.Stderr.WriteString("usage: ircc [keep]\n")
	return 2
}
