package filter

import (
	"context"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/openliq/cctp-relayer/internal/constant"
	"github.com/openliq/cctp-relayer/pkg/abi"
	"github.com/openliq/cctp-relayer/pkg/blockstore"
	"github.com/openliq/cctp-relayer/pkg/eth"
	"github.com/openliq/cctp-relayer/pkg/utils"
	"math/big"
	"strconv"
	"strings"
	"time"
)

type EventSig string

func (es EventSig) GetTopic() common.Hash {
	return crypto.Keccak256Hash([]byte(es))
}

type Msg struct {
	Token     string
	TxHash    string
	ToChainId string
	Messages  [2][]byte
}

type Config struct {
	Id, Name, Endpoint, BlockConfirmations, Contract, Events string
}

type Filter interface {
	GetEvent() error
	Stop()
}

type filter struct {
	cfg      *Config
	log      log.Logger
	contract []common.Address
	events   []EventSig
	conn     eth.Conner
	confirm  *big.Int
	ab       *abi.Abi
	bs       blockstore.BlockStorer
	ch       chan<- *Msg
	stop     chan struct{}
}

func New(cfg *Config, conn eth.Conner, l log.Logger, ab *abi.Abi, ch chan<- *Msg) (Filter, error) {
	bs, err := blockstore.New(blockstore.PathPostfix, cfg.Id)
	if err != nil {
		return nil, err
	}

	bf, ok := new(big.Int).SetString(cfg.BlockConfirmations, 10)
	if !ok {
		return nil, errors.New("blockConfirmations format failed")
	}

	events := make([]EventSig, 0)
	vs := strings.Split(cfg.Events, "|")
	for _, s := range vs {
		events = append(events, EventSig(s))
	}
	contracts := make([]common.Address, 0)
	for _, addr := range strings.Split(cfg.Contract, ",") {
		contracts = append(contracts, common.HexToAddress(addr))
	}
	return &filter{
		cfg:      cfg,
		conn:     conn,
		log:      l.New("chain", cfg.Name),
		contract: contracts,
		events:   events,
		confirm:  bf,
		bs:       bs,
		ch:       ch,
		ab:       ab,
		stop:     make(chan struct{}),
	}, nil
}

func (f *filter) Stop() {
	close(f.stop)
}

func (f *filter) GetEvent() error {
	currentBlock := big.NewInt(0)
	local, err := f.bs.TryLoadLatestBlock()
	if err != nil {
		return err
	}
	if local.Cmp(currentBlock) == 1 {
		currentBlock = local
	}
	go func() {
		for {
			select {
			case <-f.stop:
				return
			default:
				latestBlock, err := f.conn.Client().BlockNumber(context.Background())
				if err != nil {
					f.log.Error("Unable to get latest block", "block", currentBlock, "err", err)
					time.Sleep(constant.RetryInterval)
					continue
				}
				if latestBlock-currentBlock.Uint64() < f.confirm.Uint64() {
					f.log.Debug("Block not ready, will retry", "currentBlock", currentBlock, "latest", latestBlock)
					time.Sleep(constant.RetryInterval)
					continue
				}

				err = f.mosHandler(currentBlock)
				if err != nil && !errors.Is(err, types.ErrInvalidSig) {
					f.log.Error("Failed to get events for block", "block", currentBlock, "err", err)
					utils.Alarm(context.Background(), fmt.Sprintf("mos failed, chain=%s, err is %s", f.cfg.Name, err.Error()))
					time.Sleep(constant.RetryInterval)
					continue
				}

				err = f.bs.StoreBlock(currentBlock)
				if err != nil {
					f.log.Error("Failed to write latest block to blockStore", "block", currentBlock, "err", err)
				}

				currentBlock.Add(currentBlock, big.NewInt(1))
			}
		}
	}()
	return nil
}

func (f *filter) mosHandler(latestBlock *big.Int) error {
	query := f.BuildQuery(latestBlock, latestBlock)
	// querying for logs
	logs, err := f.conn.Client().FilterLogs(context.Background(), query)
	if err != nil {
		return fmt.Errorf("unable to Filter Logs: %w", err)
	}
	if len(logs) == 0 {
		return nil
	}

	for _, l := range logs {
		if !exist(l.Address, f.contract) {
			f.log.Debug("Ignore log, because address not match", "blockNumber", l.BlockNumber, "logAddress", l.Address)
			continue
		}
		if !existTopic(l.Topics[0], f.events) {
			f.log.Debug("Ignore log, because address not match", "blockNumber", l.BlockNumber, "logTopic", l.Topics[0])
			continue
		}
		// 组装
		amount := big.NewInt(0).SetBytes(l.Topics[1].Bytes())
		destChain := big.NewInt(0).SetBytes(l.Topics[2].Bytes())
		burnToken := common.BytesToAddress(l.Topics[3].Bytes())

		datas, err := f.ab.UnpackValues(constant.AbiMethodCCTPBridge, l.Data)
		if err != nil {
			f.log.Error("UnpackValues failed", "hash", l.TxHash.Hex())
			continue
		}

		crossId := datas[2].([32]byte)
		mintRecipient := make([]byte, 0, 32)
		for _, v := range crossId {
			mintRecipient = append(mintRecipient, v)
		}
		f.log.Info("Find a log", "blockHeight", strconv.FormatUint(l.BlockNumber, 10), "txHash", l.TxHash.Hex())
		f.log.Debug("Find a log", "txHash", l.TxHash.Hex(), "amount", amount, "destChain", destChain, "burnToken", burnToken,
			"_burnNoce", datas[0].(*big.Int), "_messageNonce", datas[1].(*big.Int), "mintRecipient", common.BytesToAddress(mintRecipient))
		messages, err := f.findMessageSent(logs)
		if err != nil {
			f.log.Error("FindMessageSent failed", "hash", l.TxHash.Hex())
			continue
		}
		f.log.Info("Transfer a new message", "hash", l.TxHash.Hex())
		f.ch <- &Msg{
			TxHash:    l.TxHash.String(),
			Token:     constant.Usdc[destChain.String()],
			ToChainId: constant.UsdcCIdMapping[destChain.String()],
			Messages:  messages,
		}
	}

	return nil
}

func (f *filter) findMessageSent(logs []types.Log) ([2][]byte, error) {
	var ret [2][]byte
	i := 0
	for _, l := range logs {
		if !existTopic(l.Topics[0], []EventSig{"MessageSent(bytes)"}) {
			f.log.Debug("Ignore log, because address not match", "blockNumber", l.BlockNumber, "logTopic", l.Topics[0])
			continue
		}
		ret[i] = l.Data
		i++
		if i == 2 {
			break
		}
	}

	return ret, nil
}

func (f *filter) BuildQuery(startBlock *big.Int, endBlock *big.Int) ethereum.FilterQuery {
	query := ethereum.FilterQuery{
		FromBlock: startBlock,
		ToBlock:   endBlock,
	}
	return query
}

func exist(target common.Address, dst []common.Address) bool {
	for _, d := range dst {
		if target == d {
			return true
		}
	}
	return false
}

func existTopic(target common.Hash, dst []EventSig) bool {
	for _, d := range dst {
		if target == d.GetTopic() {
			return true
		}
	}
	return false
}
