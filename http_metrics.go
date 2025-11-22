package gooctopi

import (
	"expvar"
	"net/http"
)

// metricsTransport records upstream HTTP response codes into the provided expvar map.
type metricsTransport struct {
	Base    http.RoundTripper
	Counter *expvar.Map
}

func (t *metricsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp != nil {
		incrementResponseCount(t.Counter, resp.StatusCode)
	}
	return resp, nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
