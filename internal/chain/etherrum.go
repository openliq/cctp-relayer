package chain

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/openliq/cctp-relayer/config"
	"github.com/openliq/cctp-relayer/internal/cctp"
	"github.com/openliq/cctp-relayer/internal/constant"
	"github.com/openliq/cctp-relayer/internal/crypto/secp256k1"
	"github.com/openliq/cctp-relayer/internal/filter"
	"github.com/openliq/cctp-relayer/internal/keystore"
	"github.com/openliq/cctp-relayer/internal/router"
	"github.com/openliq/cctp-relayer/internal/writer"
	"github.com/openliq/cctp-relayer/pkg/abi"
	"github.com/openliq/cctp-relayer/pkg/eth"
	"github.com/openliq/cctp-relayer/pkg/msg"
	"github.com/pkg/errors"
	"strings"
)

type ethereum struct {
	log        log.Logger
	cctpClient cctp.Cctper
	msgAbi     *abi.Abi
	bridgeAbi  *abi.Abi
	f          filter.Filter
	router     router.Router
	writer     writer.Writer
	msgCh      chan struct{}
	stop       chan struct{}
	receive    <-chan *filter.Msg
	cfg        *config.RawChainConfig
}

func newEthereum(cfg *config.RawChainConfig, l log.Logger, cli cctp.Cctper) (Chainer, error) {
	kpI, err := keystore.KeypairFromAddress(cfg.From, keystore.EthChain, "./")
	if err != nil {
		return nil, err
	}
	kp, _ := kpI.(*secp256k1.Keypair)
	conn, err := eth.NewConn(&eth.Config{
		Name:          cfg.Name,
		Endpoint:      cfg.Endpoint,
		GasLimit:      cfg.Opts.GasLimit,
		GasPrice:      cfg.Opts.MaxGasPrice,
		GasMultiplier: cfg.Opts.GasMultiplier,
		Kp:            kp,
	})
	if err != nil {
		return nil, err
	}
	err = conn.Connect()
	if err != nil {
		return nil, errors.Wrap(err, "connect failed, endpoint is "+cfg.Endpoint)
	}

	ch := make(chan *filter.Msg)
	ab, err := abi.New(constant.BridgeAbi)
	if err != nil {
		return nil, errors.Wrap(err, "create abi failed")
	}
	f, err := filter.New(&filter.Config{
		Id:                 cfg.Id,
		Name:               cfg.Name,
		Endpoint:           cfg.Endpoint,
		BlockConfirmations: cfg.Opts.BlockConfirmations,
		Contract:           cfg.Opts.Mcs,
		Events:             cfg.Opts.Event,
	}, conn, l, ab, ch)
	if err != nil {
		return nil, errors.Wrap(err, "create filter failed")
	}

	w := writer.New(conn, &writer.Config{
		Id:       cfg.Id,
		Name:     cfg.Name,
		Contract: strings.Split(cfg.Opts.Mcs, ","),
	})

	msgAbi, err := abi.New(constant.MessageAbi)
	if err != nil {
		return nil, errors.Wrap(err, "new msg abi failed")
	}
	return &ethereum{
		f:          f,
		cfg:        cfg,
		receive:    ch,
		cctpClient: cli,
		msgAbi:     msgAbi,
		bridgeAbi:  ab,
		writer:     w,
		stop:       make(chan struct{}),
		msgCh:      make(chan struct{}),
		log:        log.New("chain", cfg.Name),
	}, nil
}

func (e *ethereum) SetRouter(r router.Router) {
	e.router = r
	r.Listen(e.cfg.Id, e.writer)
}

func (e *ethereum) Id() string {
	return e.cfg.Id
}

func (e *ethereum) Name() string {
	return e.cfg.Name
}

func (e *ethereum) Start() error {
	err := e.f.GetEvent()
	if err != nil {
		return errors.Wrap(err, "start filter failed")
	}
	go func() {
		e.relayer()
	}()
	e.log.Info("Starting relay ...")
	return nil
}

func (e *ethereum) Stop() {
	e.f.Stop()
	close(e.stop)
}

func (e *ethereum) relayer() {
	for {
		select {
		case <-e.stop:
			e.log.Info("stop chain")
			return
		case receive := <-e.receive:
			var messages [2][]byte
			var signatures [2][]byte
			e.log.Info("Receive a new msg", "txHash", receive.TxHash, "token", receive.Token)
			for idx, m := range receive.Messages {
				values, err := e.msgAbi.UnpackValues(constant.AbiMethodMessageSent, m)
				if err != nil {
					e.log.Error("UnpackValues failed", "txHash", receive.TxHash, "idx", idx)
					break
				}
				// req
				messageHash := crypto.Keccak256Hash(values[0].([]byte)).Hex()
				res, err := e.cctpClient.GetProof(messageHash)
				if err != nil {
					e.log.Error("GetProof failed", "txHash", receive.TxHash, "message", messageHash)
					break
				}
				e.log.Debug("GetProof", "res", res, "message", messageHash)
				messages[idx] = values[0].([]byte)
				signatures[idx] = common.Hex2Bytes(strings.TrimPrefix(res.Attestation, "0x"))
			}

			payload, err := e.bridgeAbi.PackInput(constant.AbiMethodOnReceive, common.HexToAddress(receive.Token), messages, signatures)
			if err != nil {
				e.log.Error("PackInput failed", "txHash", receive.TxHash)
				break
			}

			msgPayload := []interface{}{payload, receive.TxHash}
			_ = e.router.Send(msg.NewSwapWithMapProof(e.cfg.Id, receive.ToChainId, 0, msgPayload, e.msgCh))
			_ = e.waitUntilMsgHandled(1)
		}
	}
}

func (e *ethereum) waitUntilMsgHandled(counter int) error {
	e.log.Debug("waitUntilMsgHandled", "counter", counter)
	for counter > 0 {
		<-e.msgCh
		counter -= 1
	}
	return nil
}
