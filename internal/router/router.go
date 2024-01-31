package router

import (
	"fmt"
	"github.com/ethereum/go-ethereum/log"
	"github.com/openliq/cctp-relayer/internal/writer"
	"github.com/openliq/cctp-relayer/pkg/msg"
	"sync"
)

type Router interface {
	Send(message msg.Message) error
	Listen(id string, w writer.Writer)
}

type router struct {
	registry map[string]msg.Handler
	lock     *sync.RWMutex
	log      log.Logger
}

func New() Router {
	return &router{
		registry: make(map[string]msg.Handler),
		lock:     &sync.RWMutex{},
		log:      log.New("system", "router"),
	}
}

func (r *router) Send(msg msg.Message) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.log.Trace("Routing message", "src", msg.Source, "dest", msg.Destination)
	dest := msg.Destination

	w := r.registry[dest]
	if w == nil {
		return fmt.Errorf("unknown destination chainId: %s", msg.Destination)
	}

	go w.ResolveMessage(msg)
	return nil
}

// Listen registers a Writer with a ChainId which Router.Send can then use to propagate messages
func (r *router) Listen(id string, w writer.Writer) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.log.Debug("Registering new chain in router", "id", id)
	r.registry[id] = w
}
