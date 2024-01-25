package msg

type Handler interface {
	ResolveMessage(message Message) bool
}

type TransferType string

var (
	SwapWithMapProof TransferType = "SwapWithProof"
)

type Message struct {
	Idx         int
	Source      string          // Source where message was initiated
	Destination string          // Destination chain of message
	Type        TransferType    // type of bridge transfer
	Payload     []interface{}   // data associated with event sequence
	DoneCh      chan<- struct{} // notify message is handled
}

func NewSwapWithMapProof(fromChainID, toChainID string, idx int, payloads []interface{}, ch chan<- struct{}) Message {
	return Message{
		Source:      fromChainID,
		Destination: toChainID,
		Type:        SwapWithMapProof,
		Payload:     payloads,
		DoneCh:      ch,
		Idx:         idx,
	}
}
