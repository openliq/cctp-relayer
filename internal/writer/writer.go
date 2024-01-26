package writer

import (
	"context"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/openliq/cctp-relayer/internal/constant"
	"github.com/openliq/cctp-relayer/pkg/eth"
	"github.com/openliq/cctp-relayer/pkg/msg"
	"github.com/openliq/cctp-relayer/pkg/utils"
	"math/big"
	"strings"
	"time"
)

type Writer interface {
	ResolveMessage(message msg.Message) bool
	Close()
}

type Config struct {
	Id       string
	Name     string
	Contract []string
}

type writer struct {
	cfg  Config
	conn eth.Conner
	log  log.Logger
	stop chan struct{}
}

// New creates and returns Writer
func New(conn eth.Conner, cfg *Config) Writer {
	return &writer{
		cfg:  *cfg,
		conn: conn,
		log:  log.New("writer", cfg.Name),
		stop: make(chan struct{}),
	}
}

func (w *writer) ResolveMessage(m msg.Message) bool {
	w.log.Info("Attempting to resolve message", "type", m.Type, "src", m.Source, "dst", m.Destination)

	switch m.Type {
	case msg.SwapWithMapProof:
		return w.exeSwapMsg(m)
	default:
		w.log.Error("Unknown message type received", "type", m.Type)
		return false
	}
}

func (w *writer) Close() {
	close(w.stop)
}

// exeSwapMsg contract using address and function signature with message info
func (w *writer) exeSwapMsg(m msg.Message) bool {
	var (
		errorCount int64
		needNonce  = true
		addr       = common.HexToAddress(w.cfg.Contract[m.Idx])
	)
	for {
		select {
		case <-w.stop:
			return false
		default:
			err := w.conn.LockAndUpdateOpts(needNonce)
			if err != nil {
				w.log.Error("Failed to update nonce", "err", err)
				time.Sleep(constant.RetryInterval)
				continue
			}

			var inputHash interface{}
			if len(m.Payload) > 1 {
				inputHash = m.Payload[1]
			}
			w.log.Info("Send transaction", "addr", addr, "srcHash", inputHash, "needNonce", needNonce, "nonce", w.conn.Opts().Nonce)
			mcsTx, err := w.sendTx(&addr, nil, m.Payload[0].([]byte))
			if err == nil {
				w.log.Info("Submitted cross tx execution", "src", m.Source, "dst", m.Destination, "srcHash", inputHash, "mcsTx", mcsTx.Hash())
				err = w.txStatus(mcsTx.Hash())
				if err != nil {
					w.log.Warn("TxHash Status is not successful, will retry", "err", err)
				} else {
					m.DoneCh <- struct{}{}
					return true
				}
			} else {
				for e := range constant.IgnoreError {
					if strings.Index(err.Error(), e) != -1 {
						w.log.Info("Ignore This Error, Continue to the next", "id", m.Destination, "err", err)
						m.DoneCh <- struct{}{}
						return true
					}
				}
				w.log.Warn("Execution failed, will retry", "srcHash", inputHash, "err", err)
			}
			needNonce = w.needNonce(err)
			errorCount++
			if errorCount >= 10 {
				w.mosAlarm(m, inputHash, err)
				errorCount = 0
			}
			time.Sleep(constant.RetryInterval)
		}
	}
}

func (w *writer) sendTx(toAddress *common.Address, value *big.Int, input []byte) (*types.Transaction, error) {
	gasPrice := w.conn.Opts().GasPrice
	nonce := w.conn.Opts().Nonce
	from := w.conn.Keypair().CommonAddress()

	cm := ethereum.CallMsg{
		From:     from,
		To:       toAddress,
		GasPrice: gasPrice,
		Value:    value,
		Data:     input,
	}
	gasLimit, err := w.conn.Client().EstimateGas(context.Background(), cm)
	if err != nil {
		w.log.Error("EstimateGas failed sendTx", "error:", err.Error())
		return nil, err
	}

	gasTipCap := w.conn.Opts().GasTipCap
	gasFeeCap := w.conn.Opts().GasFeeCap
	//if w.cfg.LimitMultiplier > 1 {
	//	gasLimit = uint64(float64(gasLimit) * w.cfg.LimitMultiplier)
	//}
	//if w.cfg.GasMultiplier > 1 && gasTipCap != nil && gasFeeCap != nil {
	//	gasTipCap = big.NewInt(int64(float64(gasTipCap.Uint64()) * w.cfg.GasMultiplier))
	//	gasFeeCap = big.NewInt(int64(float64(gasFeeCap.Uint64()) * w.cfg.GasMultiplier))
	//}
	w.log.Info("SendTx gasPrice", "gasPrice", gasPrice,
		"gasTipCap", gasTipCap, "gasFeeCap", gasFeeCap, "gasLimit", gasLimit)
	//,"limitMultiplier", w.cfg.LimitMultiplier, "gasMultiplier", w.cfg.GasMultiplier)
	// td interface
	var td types.TxData
	// EIP-1559
	if gasPrice != nil {
		// legacy branch
		td = &types.LegacyTx{
			Nonce:    nonce.Uint64(),
			Value:    value,
			To:       toAddress,
			Gas:      gasLimit,
			GasPrice: gasPrice,
			Data:     input,
		}
	} else {
		// london branch
		td = &types.DynamicFeeTx{
			Nonce:     nonce.Uint64(),
			Value:     value,
			To:        toAddress,
			Gas:       gasLimit,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Data:      input,
		}
	}

	tx := types.NewTx(td)
	chainID, _ := big.NewInt(0).SetString(w.cfg.Id, 10)
	privateKey := w.conn.Keypair().PrivateKey()

	signedTx, err := types.SignTx(tx, types.NewLondonSigner(chainID), privateKey)
	if err != nil {
		w.log.Error("SignTx failed", "error:", err.Error())
		return nil, err
	}

	err = w.conn.Client().SendTransaction(context.Background(), signedTx)
	if err != nil {
		w.log.Error("SendTransaction failed", "error:", err.Error())
		return nil, err
	}
	return signedTx, nil
}

func (w *writer) txStatus(txHash common.Hash) error {
	var count int64
	//time.Sleep(time.Second * 2)
	for {
		_, pending, err := w.conn.Client().TransactionByHash(context.Background(), txHash) // Query whether it is on the chain
		if pending {
			w.log.Info("Tx is Pending, please wait...", "tx", txHash.Hex())
			time.Sleep(constant.QueryRetryInterval)
			count++
			if count == 60 {
				return errors.New("The Tx pending state is too long")
			}
			continue
		}
		if err != nil {
			time.Sleep(constant.QueryRetryInterval)
			count++
			if count == 60 {
				return err
			}
			w.log.Error("Tx Found failed, please wait...", "txHash", txHash, "err", err)
			continue
		}
		break
	}
	count = 0
	for {
		receipt, err := w.conn.Client().TransactionReceipt(context.Background(), txHash)
		if err != nil {
			if strings.Index(err.Error(), "not found") != -1 {
				w.log.Info("Tx is temporary not found, please wait...", "tx", txHash)
				time.Sleep(constant.QueryRetryInterval)
				count++
				if count == 40 {
					return err
				}
				continue
			}
			return err
		}

		if receipt.Status == types.ReceiptStatusSuccessful {
			w.log.Info("Tx receipt status is success", "txHash", txHash.Hex())
			return nil
		}
		return fmt.Errorf("txHash(%s), status not success, current status is (%d)", txHash, receipt.Status)
	}
}

func (w *writer) needNonce(err error) bool {
	if err == nil || err.Error() == constant.ErrNonceTooLow.Error() || strings.Index(err.Error(), "nonce too low") != -1 {
		return true
	}

	return false
}

func (w *writer) mosAlarm(m msg.Message, tx interface{}, err error) {
	utils.Alarm(context.Background(), fmt.Sprintf("mos %s2%s failed, srcHash=%v err is %s", constant.OnlineChaId[m.Source],
		constant.OnlineChaId[m.Destination], tx, err.Error()))
}
