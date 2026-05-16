package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/xs23933/core/v3/gen"
	"gopkg.in/yaml.v3"
)

func main() {
	var (
		configFile string
		outputDir  string
		pkgName    string
	)

	flag.StringVar(&configFile, "f", "enums.yaml", "enum config file (yaml)")
	flag.StringVar(&outputDir, "o", "", "output directory (default: current dir)")
	flag.StringVar(&pkgName, "p", "", "package name (default: from config file or 'constants')")
	flag.Parse()

	data, err := os.ReadFile(configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read config: %v\n", err)
		os.Exit(1)
	}

	var cfg gen.EnumConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "parse config: %v\n", err)
		os.Exit(1)
	}

	if outputDir != "" {
		cfg.Output = outputDir
	}
	if pkgName != "" {
		cfg.Package = pkgName
	}

	if len(cfg.Enums) == 0 {
		fmt.Fprintln(os.Stderr, "no enums defined in config")
		os.Exit(1)
	}

	if err := gen.GenerateEnums(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("done")
}
