package main

import (
	"flag"
	"os"
)

func main() { os.Exit(run(os.Args[1:], nil)) }

func run(args []string, ready func()) int {
	if len(args) > 0 && args[0] == "keep" {
		return runKeep(ready)
	}
	var nick string
	fs := flag.NewFlagSet("ircc", flag.ContinueOnError)
	fs.StringVar(&nick, "n", "", "user nick (default $USER)")
	fs.StringVar(&nick, "nick", "", "user nick (default $USER)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		os.Stderr.WriteString("usage: ircc [-n nick] [keep]\n")
		return 2
	}
	return runTUI(nick)
}
