package httpclient

type refusalClass uint8

const (
	classUnset refusalClass = iota
	classTerminal
	classRetryable
)

type refusalTable map[string]refusalClass

var (
	nodeTopologyRefusals      = refusalTable{}
	offerAvailabilityRefusals = refusalTable{}
	shippingTrackingRefusals  = refusalTable{}
)
