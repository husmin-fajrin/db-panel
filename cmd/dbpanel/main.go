// Perintah dbpanel: satu biner dengan dua peran (panel web dan agent server).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/panel"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

const usage = `Penggunaan: dbpanel <panel|agent|version>

  panel    jalankan panel web
  agent    jalankan agent server
  version  tampilkan versi
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "dbpanel %s\n", version.Version)
		return 0
	case "agent":
		return agent.Main(args[1:], stdout, stderr)
	case "panel":
		return panel.Main(args, stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}
