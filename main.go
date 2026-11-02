// tmui is a terminal UI for tmux: sessions, windows and panes in one
// screen, and attach that comes back.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sophonz-labs/tmui/internal/plugin"
	"github.com/sophonz-labs/tmui/internal/ui"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ext" {
		os.Exit(plugin.Main("tmui", ui.Builtins(), os.Args[2:], os.Stdin, os.Stdout))
	}
	fs := flag.NewFlagSet("tmui", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `tmui: a terminal UI for tmux sessions, windows and panes

usage: tmui [-L socket-name | -S socket-path]
       tmui ext list | install <name|path|url> | remove <name>

  -L name     use the tmux server on socket name (as tmux -L)
  -S path     use the tmux server at socket path (as tmux -S)
  --version   print the version
  --help      this

Without -L or -S, tmui shows the server it runs in ($TMUX), else the
default server. Press ? inside for the keys.
`)
	}
	socket := fs.String("L", "", "")
	path := fs.String("S", "", "")
	showVersion := fs.Bool("version", false, "")
	fs.BoolVar(showVersion, "V", false, "")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2)
	}
	if *showVersion {
		fmt.Println("tmui", version)
		return
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "tmui: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		os.Exit(2)
	}
	if *socket != "" && *path != "" {
		fmt.Fprintln(os.Stderr, "tmui: -L and -S are either-or")
		os.Exit(2)
	}
	os.Exit(ui.Run(ui.Options{Socket: *socket, SocketPath: *path, Version: version}))
}
