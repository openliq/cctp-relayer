package flag

import "github.com/urfave/cli/v2"

var (
	ConfigFile = &cli.StringFlag{
		Name:  "config",
		Usage: "JSON configuration file",
	}
)
