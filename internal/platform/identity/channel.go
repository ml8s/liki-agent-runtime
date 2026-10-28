package identity

import "net/http"

// ChannelSource routes identity extraction by verified transport channel.
// Mutually authenticated direct connections use the certificate source; every
// other channel falls back to the relay-header source. Composition decides
// once, at startup, which adapters are combined; protocol handlers keep a
// single RequestSource port.
type ChannelSource struct {
	relay  RequestSource
	direct RequestSource
}

// NewChannelSource combines the plain-channel relay source with the
// mutual-TLS direct source. Nil sources reject their channel instead of
// falling through, so a misconfigured composition fails closed.
func NewChannelSource(relay, direct RequestSource) ChannelSource {
	return ChannelSource{relay: relay, direct: direct}
}

// IdentityFromRequest implements RequestSource.
func (source ChannelSource) IdentityFromRequest(request *http.Request) (Identity, bool) {
	if request == nil {
		return Identity{}, false
	}
	if MutualTLSVerified(request) {
		if source.direct == nil {
			return Identity{}, false
		}
		return source.direct.IdentityFromRequest(request)
	}
	if source.relay == nil {
		return Identity{}, false
	}
	return source.relay.IdentityFromRequest(request)
}
