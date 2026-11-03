package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `usage: ircc [-n nick] [-room &name] [-invite nick,...] [-h] [keep]

  -n, --nick nick       user nick (default $USER; a taken name gets a suffix)
  -room &name           open this room at start and keep it open
  -invite nick,...      invite these sessions to -room as soon as each is awake
  -h, --help            show this help
`

// options are the flags of the TUI run.
type options struct {
	nick   string
	room   string
	invite []string
}

func main() { os.Exit(run(os.Args[1:], nil)) }

func run(args []string, ready func()) int {
	if len(args) > 0 && args[0] == "keep" {
		return runKeep(ready)
	}
	var o options
	var invite string
	fs := flag.NewFlagSet("ircc", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // flag's own error and usage text: we print ours
	fs.Usage = func() {}
	fs.StringVar(&o.nick, "n", "", "user nick (default $USER)")
	fs.StringVar(&o.nick, "nick", "", "user nick (default $USER)")
	fs.StringVar(&o.room, "room", "", "room to open at start")
	fs.StringVar(&invite, "invite", "", "comma-separated nicks to invite to -room")
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
	if invite != "" && o.room == "" {
		fmt.Fprintln(os.Stderr, "ircc: -invite needs -room")
		return 2
	}
	o.invite = strings.Split(invite, ",")
	if invite == "" {
		o.invite = nil
	}
	return runTUI(o)
}
