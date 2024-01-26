package filter

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/openliq/cctp-relayer/internal/constant"
	"github.com/openliq/cctp-relayer/pkg/abi"
	"github.com/openliq/cctp-relayer/pkg/blockstore"
	"github.com/openliq/cctp-relayer/pkg/contract"
	"github.com/openliq/cctp-relayer/pkg/eth"
	"github.com/openliq/cctp-relayer/pkg/utils"
	"github.com/pkg/errors"
	"math/big"
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
	msgAbi   *abi.Abi
	bs       blockstore.BlockStorer
	call     *contract.Call
	ch       chan<- *Msg
	stop     chan struct{}
}

func New(cfg *Config, conn eth.Conner, l log.Logger, ab *abi.Abi, ch chan<- *Msg) (Filter, error) {
	msgAbi, err := abi.New(constant.MessageAbi)
	if err != nil {
		return nil, errors.Wrap(err, "new msg abi failed")
	}
	ret := &filter{
		cfg:    cfg,
		conn:   conn,
		log:    l.New("chain", cfg.Name),
		ch:     ch,
		ab:     ab,
		msgAbi: msgAbi,
		stop:   make(chan struct{}),
	}
	err = ret.parseConfig()
	if err != nil {
		return nil, err
	}
	ret.call = contract.NewCall(conn, ret.contract[0], ret.ab)
	return ret, nil
}

func (f *filter) parseConfig() error {
	bs, err := blockstore.New(blockstore.PathPostfix, f.cfg.Id)
	if err != nil {
		return err
	}

	bf, ok := new(big.Int).SetString(f.cfg.BlockConfirmations, 10)
	if !ok {
		return errors.New("blockConfirmations format failed")
	}

	events := make([]EventSig, 0)
	vs := strings.Split(f.cfg.Events, "|")
	for _, s := range vs {
		events = append(events, EventSig(s))
	}
	contracts := make([]common.Address, 0)
	for _, addr := range strings.Split(f.cfg.Contract, ",") {
		contracts = append(contracts, common.HexToAddress(addr))
	}
	f.contract = contracts
	f.events = events
	f.confirm = bf
	f.bs = bs
	return nil
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
		f.log.Info("Find a log", "blockHeight", l.BlockNumber, "txHash", l.TxHash.Hex(), "destChain", destChain)
		f.log.Debug("Find a log", "txHash", l.TxHash.Hex(), "amount", amount, "burnToken", burnToken, "mintRecipient", common.BytesToAddress(mintRecipient))
		messages, err := f.findMessageSent(logs, datas[0].(*big.Int), datas[1].(*big.Int))
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

func (f *filter) findMessageSent(logs []types.Log, burnNonce, messageNonce *big.Int) ([2][]byte, error) {
	var ret [2][]byte
	i := 0
	for _, l := range logs {
		if !existTopic(l.Topics[0], []EventSig{"MessageSent(bytes)"}) {
			f.log.Debug("Ignore log, because address not match", "blockNumber", l.BlockNumber, "logTopic", l.Topics[0])
			continue
		}
		values, err := f.msgAbi.UnpackValues(constant.AbiMethodMessageSent, l.Data)
		if err != nil {
			f.log.Error("UnpackValues failed", "txHash", l.TxHash.Hex())
			continue
		}
		nonce := new(big.Int)
		err = f.call.Call(constant.AbiMethodGetNonce, &nonce, values[0].([]byte))
		if err != nil {
			f.log.Error("To get message nonce failed", "txHash", l.TxHash.Hex(), "err", err)
			continue
		}
		if nonce.Uint64() == burnNonce.Uint64() || nonce.Uint64() == messageNonce.Uint64() {
			f.log.Info("find nonce same message", "txHash", l.TxHash.Hex(), "msgNonce", nonce, "burnNonce", burnNonce, "messageNonce", messageNonce)
			ret[i] = values[0].([]byte)
			i++
			if i == 2 {
				break
			}
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
