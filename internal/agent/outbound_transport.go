package agent

import (
	"crypto/tls"
	"net/http"
)

// newOutboundTransport returns the shared HTTP transport for outbound LLM and
// MCP traffic. It clones the standard transport so proxy, HTTP/2, and timeout
// defaults are preserved, then sizes the per-host idle pool from the run
// concurrency bound.
func newOutboundTransport(maxConcurrentRuns int) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tuned := base.Clone()
	perHost := max(64, min(512, 8*maxConcurrentRuns))
	tuned.MaxIdleConns = max(tuned.MaxIdleConns, perHost)
	tuned.MaxIdleConnsPerHost = perHost
	if tuned.TLSClientConfig == nil {
		tuned.TLSClientConfig = &tls.Config{}
	}
	tuned.TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(0)
	return tuned
}
