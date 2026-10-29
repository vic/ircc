package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const usage = `usage: ircc [-n nick] [-h] [keep | mcp]

  mcp              MCP stdio bridge to the running TUI (Claude Code: command ircc, args [mcp])

  -n, --nick nick  user nick (default $USER; a taken name gets a suffix)
  -h, --help       show this help
`

func main() { os.Exit(run(os.Args[1:], nil)) }

func run(args []string, ready func()) int {
	if len(args) > 0 && args[0] == "keep" {
		return runKeep(ready)
	}
	if len(args) == 1 && args[0] == "mcp" {
		return runMCP()
	}
	var nick string
	fs := flag.NewFlagSet("ircc", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // flag's own error and usage text: we print ours
	fs.Usage = func() {}
	fs.StringVar(&nick, "n", "", "user nick (default $USER)")
	fs.StringVar(&nick, "nick", "", "user nick (default $USER)")
	help := fs.Bool("h", false, "show this help")
	fs.BoolVar(help, "help", false, "show this help")
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) || *help {
		fmt.Fprint(os.Stdout, usage)
		return 0
	} else if err != nil || fs.NArg() > 0 {
		if err != nil {
			fmt.Fprintln(os.Stderr, "ircc:", err)
		}
		os.Stderr.WriteString(usage)
		return 2
	}
	return runTUI(nick)
}
