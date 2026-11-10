package main

import (
	"flag"
	"os"

	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/example/standard/internal/app"
)

func main() {
	config := flag.String("f", "config.yaml", "configuration file")
	migrate := flag.Bool("migrate", false, "create/update the demo schema and exit")
	flag.Parse()
	if err := app.Run(*config, *migrate); err != nil {
		core.Erro("application failed: %v", err)
		os.Exit(1)
	}
}
