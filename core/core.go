package core

import (
	"fmt"
	"github.com/ethereum/go-ethereum/log"
	"github.com/openliq/cctp-relayer/internal/chain"
	"github.com/openliq/cctp-relayer/internal/router"
	"os"
	"os/signal"
	"syscall"
)

type Core struct {
	Registry []chain.Chainer
	route    router.Router
	log      log.Logger
	sysErr   <-chan error
}

func New(sysErr <-chan error) *Core {
	return &Core{
		Registry: make([]chain.Chainer, 0),
		route:    router.New(),
		log:      log.New("system", "core"),
		sysErr:   sysErr,
	}
}

// AddChain registers the chain in the Registry and calls Chain.SetRouter()
func (c *Core) AddChain(chain chain.Chainer) {
	c.Registry = append(c.Registry, chain)
	chain.SetRouter(c.route)
}

// Start will call all registered chains' Start methods and block forever (or until signal is received)
func (c *Core) Start() {
	for _, ch := range c.Registry {
		err := ch.Start()
		if err != nil {
			c.log.Error("failed to start chain", "chain", ch.Id(), "err", err)
			return
		}
		c.log.Info(fmt.Sprintf("Started %s chain", ch.Name()))
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigc)

	// Block here and wait for a signal
	select {
	case err := <-c.sysErr:
		c.log.Error("FATAL ERROR. Shutting down.", "err", err)
	case <-sigc:
		c.log.Warn("Interrupt received, shutting down now.")
	}

	// Signal chains to shutdown
	for _, ch := range c.Registry {
		ch.Stop()
	}
}

func (c *Core) Errors() <-chan error {
	return c.sysErr
}
