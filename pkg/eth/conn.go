package eth

import (
	"context"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/openliq/cctp-relayer/internal/crypto/secp256k1"
	"github.com/pkg/errors"
	"math/big"
	"strconv"
	"sync"
)

type Conner interface {
	Client() *ethclient.Client
	Connect() error
	LockAndUpdateOpts(needNewNonce bool) error
	Keypair() *secp256k1.Keypair
	Opts() *bind.TransactOpts
}

type Config struct {
	Name          string
	Endpoint      string
	GasLimit      string
	GasPrice      string
	GasMultiplier string
	Kp            *secp256k1.Keypair
}

type Connection struct {
	cfg           *Config
	endpoint      string
	conn          *ethclient.Client
	gasLimit      *big.Int
	maxGasPrice   *big.Int
	gasMultiplier *big.Float
	opts          *bind.TransactOpts
	callOpts      *bind.CallOpts
	optsLock      sync.Mutex
	log           log.Logger
}

// NewConn returns an uninitialized connection, must call Connection.Connect() before using.
func NewConn(cfg *Config) (Conner, error) {
	limit := big.NewInt(0)
	_, pass := limit.SetString(cfg.GasLimit, 10)
	if !pass {
		return nil, errors.New("unable to parse gas limit")
	}
	price := big.NewInt(0)
	_, pass = price.SetString(cfg.GasPrice, 10)
	if !pass {
		return nil, errors.New("unable to parse max gas price")
	}
	gasMultiplier := float64(1)
	float, err := strconv.ParseFloat(cfg.GasMultiplier, 64)
	if err == nil {
		gasMultiplier = float
	} else {
		return nil, errors.New("unable to parse gasMultiplier to float")
	}

	bigFloat := new(big.Float).SetFloat64(gasMultiplier)
	return &Connection{
		cfg:           cfg,
		gasLimit:      limit,
		maxGasPrice:   price,
		gasMultiplier: bigFloat,
		log:           log.New("conn", cfg.Name),
	}, nil
}

// Connect starts the ethereum WS connection
func (c *Connection) Connect() error {
	var (
		err       error
		rpcClient *rpc.Client
	)
	rpcClient, err = rpc.DialHTTP(c.cfg.Endpoint)
	if err != nil {
		return err
	}
	c.conn = ethclient.NewClient(rpcClient)

	opts, _, err := c.newTransactOpts(big.NewInt(0), c.gasLimit, c.maxGasPrice)
	if err != nil {
		return err
	}
	c.opts = opts
	c.callOpts = &bind.CallOpts{From: c.cfg.Kp.CommonAddress()}
	return nil
}

func (c *Connection) Client() *ethclient.Client {
	return c.conn
}

func (c *Connection) Keypair() *secp256k1.Keypair {
	return c.cfg.Kp
}

func (c *Connection) Opts() *bind.TransactOpts {
	return c.opts
}

// LatestBlock returns the latest block from the current chain
func (c *Connection) LatestBlock() (*big.Int, error) {
	bnum, err := c.conn.BlockNumber(context.Background())
	if err != nil {
		return nil, err
	}
	return big.NewInt(0).SetUint64(bnum), nil
}

// Close terminates the client connection and stops any running routines
func (c *Connection) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// newTransactOpts builds the TransactOpts for the connection's keypair.
func (c *Connection) newTransactOpts(value, gasLimit, gasPrice *big.Int) (*bind.TransactOpts, uint64, error) {
	privateKey := c.cfg.Kp.PrivateKey()
	address := crypto.PubkeyToAddress(privateKey.PublicKey)

	nonce, err := c.conn.PendingNonceAt(context.Background(), address)
	if err != nil {
		return nil, 0, err
	}

	auth := bind.NewKeyedTransactor(privateKey)
	if err != nil {
		return nil, 0, err
	}

	auth.Nonce = big.NewInt(int64(nonce))
	auth.Value = value
	auth.GasLimit = uint64(gasLimit.Int64())
	auth.GasPrice = gasPrice
	auth.Context = context.Background()

	return auth, nonce, nil
}

func (c *Connection) SafeEstimateGas(ctx context.Context) (*big.Int, error) {
	var suggestedGasPrice *big.Int
	// Fallback to the node rpc method for the gas price if GSN did not provide a price
	c.log.Debug("Fetching gasPrice from node")
	nodePriceEstimate, err := c.conn.SuggestGasPrice(context.TODO())
	if err != nil {
		return nil, err
	} else {
		suggestedGasPrice = nodePriceEstimate
	}

	gasPrice := multiplyGasPrice(suggestedGasPrice, c.gasMultiplier)

	// Check we aren't exceeding our limit
	if gasPrice.Cmp(c.maxGasPrice) == 1 {
		return c.maxGasPrice, nil
	} else {
		return gasPrice, nil
	}
}

func (c *Connection) EstimateGasLondon(ctx context.Context, baseFee *big.Int) (*big.Int, *big.Int, error) {
	var maxPriorityFeePerGas *big.Int
	var maxFeePerGas *big.Int

	if c.maxGasPrice.Cmp(baseFee) < 0 {
		maxPriorityFeePerGas = big.NewInt(1000000000)
		maxFeePerGas = new(big.Int).Add(c.maxGasPrice, maxPriorityFeePerGas)
		return maxPriorityFeePerGas, maxFeePerGas, nil
	}

	maxPriorityFeePerGas, err := c.conn.SuggestGasTipCap(context.TODO())
	if err != nil {
		return nil, nil, err
	}
	c.log.Info("EstimateGasLondon", "maxPriorityFeePerGas", maxPriorityFeePerGas)

	maxFeePerGas = new(big.Int).Add(
		maxPriorityFeePerGas,
		baseFee,
	)

	// Check we aren't exceeding our limit
	if maxFeePerGas.Cmp(c.maxGasPrice) == 1 {
		c.log.Info("EstimateGasLondon maxFeePerGas more than set", "maxFeePerGas", maxFeePerGas, "baseFee", baseFee)
		maxPriorityFeePerGas.Sub(c.maxGasPrice, baseFee)
		maxFeePerGas = c.maxGasPrice
	}
	return maxPriorityFeePerGas, maxFeePerGas, nil
}

func multiplyGasPrice(gasEstimate *big.Int, gasMultiplier *big.Float) *big.Int {
	gasEstimateFloat := new(big.Float).SetInt(gasEstimate)
	result := gasEstimateFloat.Mul(gasEstimateFloat, gasMultiplier)
	gasPrice := new(big.Int)
	result.Int(gasPrice)
	return gasPrice
}

// LockAndUpdateOpts acquires a lock on the opts before updating the nonce
// and gas price.
func (c *Connection) LockAndUpdateOpts(needNewNonce bool) error {
	//c.optsLock.Lock()
	head, err := c.conn.HeaderByNumber(context.TODO(), nil)
	// cos map chain dont have this section in return,this err will be raised
	if err != nil && err.Error() != "missing required field 'sha3Uncles' for Header" {
		c.UnlockOpts()
		c.log.Error("LockAndUpdateOpts HeaderByNumber", "err", err)
		return err
	}

	if head.BaseFee != nil {
		c.opts.GasTipCap, c.opts.GasFeeCap, err = c.EstimateGasLondon(context.TODO(), head.BaseFee)

		// Both gasPrice and (maxFeePerGas or maxPriorityFeePerGas) cannot be specified: https://github.com/ethereum/go-ethereum/blob/95bbd46eabc5d95d9fb2108ec232dd62df2f44ab/accounts/abi/bind/base.go#L254
		c.opts.GasPrice = nil
		if err != nil {
			// if EstimateGasLondon failed, fall back to suggestGasPrice
			c.opts.GasPrice, err = c.conn.SuggestGasPrice(context.TODO())
			if err != nil {
				//c.UnlockOpts()
				return err
			}
		}
		c.log.Info("LockAndUpdateOpts ", "head.BaseFee", head.BaseFee, "maxGasPrice", c.maxGasPrice)
	} else {
		var gasPrice *big.Int
		gasPrice, err = c.SafeEstimateGas(context.TODO())
		if err != nil {
			//c.UnlockOpts()
			return err
		}
		c.opts.GasPrice = gasPrice
	}

	if !needNewNonce {
		return nil
	}
	nonce, err := c.conn.PendingNonceAt(context.Background(), c.opts.From)
	if err != nil {
		//c.optsLock.Unlock()
		return err
	}
	c.opts.Nonce.SetUint64(nonce)
	return nil
}

func (c *Connection) UnlockOpts() {
	//c.optsLock.Unlock()
}
