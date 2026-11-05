// tmui is a terminal UI for tmux: sessions, windows and panes in one
// screen, and attach that comes back.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/sophonz-labs/tmui/internal/tmuxruntime"

	"github.com/sophonz-labs/tmui/internal/ui"
	"github.com/sophonz-labs/tmui/pkg/config"
	"github.com/sophonz-labs/tmui/pkg/plugin"
	"github.com/sophonz-labs/tmui/pkg/upgrade"
)

var version = "dev"

func main() {
	args, err := tmuxruntime.Configure("tmui", os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Args = append(os.Args[:1], args...)
	// Expose the same tmux chosen by the UI for diagnostics and offline use.
	if len(os.Args) > 1 && os.Args[1] == "tmux" {
		cmd := exec.Command("tmux", os.Args[2:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				os.Exit(e.ExitCode())
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "config" {
		os.Exit(config.Main("tmui", ui.StartViews(), os.Args[2:], os.Stdout))
	}
	if len(os.Args) > 1 && os.Args[1] == "ext" {
		os.Exit(plugin.Main("tmui", ui.Builtins(), os.Args[2:], os.Stdin, os.Stdout))
	}
	if len(os.Args) > 1 && os.Args[1] == "upgrade" {
		_, code := upgrade.Main("tmui", version, os.Args[2:], os.Stdout)
		os.Exit(code)
	}
	fs := flag.NewFlagSet("tmui", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `tmui: a terminal UI for tmux sessions, windows and panes

usage: tmui [--embedded-tmux] [-L socket-name | -S socket-path]
       tmui [--embedded-tmux] tmux <args>
       tmui ext list | install <name|path|url> | remove <name>
       tmui config [show] | path | edit
       tmui upgrade [--check] [--version vX.Y.Z]

  --embedded-tmux  use the tmux bundled in Linux releases
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
