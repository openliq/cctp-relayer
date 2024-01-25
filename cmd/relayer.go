package main

import (
	"github.com/ethereum/go-ethereum/log"
	"github.com/openliq/cctp-relayer/config"
	"github.com/openliq/cctp-relayer/core"
	"github.com/openliq/cctp-relayer/internal/chain"
	"github.com/openliq/cctp-relayer/internal/flag"
	"github.com/openliq/cctp-relayer/pkg/utils"
	"github.com/urfave/cli/v2"
	"os"
)

var relayerCommand = cli.Command{
	Name:        "relayer",
	Usage:       "cctp relayer",
	Description: "The relayer command is used to cctp cross chain",
	Action:      relayer,
	Flags:       append(app.Flags, flag.ConfigFile),
}

func relayer(cli *cli.Context) error {
	l := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LvlInfo, true))
	log.SetDefault(l)
	cfg, err := config.Local(cli.String(flag.ConfigFile.Name))
	if err != nil {
		log.Error("config init failed", "err", err)
		return err
	}

	utils.Init(cfg.Other.Env, cfg.Other.MonitorUrl)
	chainers, err := chain.Init(cfg, l)
	if err != nil {
		log.Error("chain init failed", "err", err)
		return err
	}

	sysErr := make(chan error)
	c := core.New(sysErr)
	for _, ch := range chainers {
		c.AddChain(ch)
	}
	c.Start()
	return nil
}
