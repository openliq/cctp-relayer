package main

import (
	"github.com/ethereum/go-ethereum/log"
	"github.com/urfave/cli/v2"
	"os"
)

var (
	app     = cli.NewApp()
	Version = "1.0.0"
)

func init() {
	app.Copyright = "Copyright 2024 MAP Protocol 2024 Authors"
	app.Name = "cctp-relayer"
	app.Usage = "cctp-relayer"
	app.Authors = []*cli.Author{{Name: "MAP Protocol 2024"}}
	app.Version = Version
	app.EnableBashCompletion = true
	app.Commands = []*cli.Command{
		&relayerCommand,
	}

}

func main() {
	if err := app.Run(os.Args); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}
