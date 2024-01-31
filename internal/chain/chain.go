package chain

import (
	"errors"
	"github.com/ethereum/go-ethereum/log"
	"github.com/openliq/cctp-relayer/config"
	"github.com/openliq/cctp-relayer/internal/cctp"
	"github.com/openliq/cctp-relayer/internal/constant"
	"github.com/openliq/cctp-relayer/internal/router"
)

type Chainer interface {
	Id() string
	Name() string
	Start() error // Start chain
	Stop()
	SetRouter(router.Router)
}

func Init(cfg *config.Config, l log.Logger) ([]Chainer, error) {
	ret := make([]Chainer, 0)
	for _, ccfg := range cfg.Chains {
		var (
			err error
			c   Chainer
		)

		ele := ccfg
		switch ccfg.Type {
		case constant.Ethereum:
			c, err = newEthereum(&ele, l, cctp.New(cfg.Other.AttestationUrl))
		default:
			return nil, errors.New("unrecognized Chain Type")
		}
		if err != nil {
			return nil, err
		}
		ret = append(ret, c)
		constant.OnlineChaId[ccfg.Id] = ccfg.Name
		constant.Usdc[constant.UsdcChainMapping[ccfg.Id]] = ccfg.Opts.Usdc
		constant.UsdcCIdMapping[constant.UsdcChainMapping[ccfg.Id]] = ccfg.Id
	}
	return ret, nil
}
