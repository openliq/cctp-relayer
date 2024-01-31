package contract

import (
	"context"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/openliq/cctp-relayer/internal/constant"
	"github.com/openliq/cctp-relayer/pkg/abi"
	"github.com/openliq/cctp-relayer/pkg/eth"
)

type Call struct {
	conn eth.Conner
	abi  *abi.Abi
	toC  common.Address
}

func NewCall(conn eth.Conner, addr common.Address, abi *abi.Abi) *Call {
	return &Call{
		conn: conn,
		toC:  addr,
		abi:  abi,
	}
}

func (c *Call) Call(method string, ret interface{}, params ...interface{}) error {
	input, err := c.abi.PackInput(method, params...)
	if err != nil {
		return err
	}

	outPut, err := c.conn.Client().CallContract(context.Background(),
		ethereum.CallMsg{
			From: constant.ZeroAddress,
			To:   &c.toC,
			Data: input,
		},
		nil,
	)
	if err != nil {
		return err
	}

	return c.abi.UnpackOutput(method, ret, outPut)
}
