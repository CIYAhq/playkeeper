package machinelink

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"time"
)

// AgentProxy forwards requests to the agent's Unix socket. A joined
// machine's link runs as the unprivileged playkeeper user with this as
// its Handler, so the code facing the network never runs as root, and
// the agent checks the link's user id as it checks the panel's.
func AgentProxy(socket string) http.Handler {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableCompression: true,
		MaxIdleConns:       8,
		IdleConnTimeout:    time.Minute,
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "agent"
			pr.Out.Host = "agent"
		},
		Transport:     tr,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return
			}
			writeAPIError(w, http.StatusBadGateway, errAgentDown(err))
		},
	}
}

func errAgentDown(err error) *Error {
	return &Error{Code: CodeAgentDown,
		Msg:  "The Playkeeper agent on this machine is not running.",
		Hint: "On that machine, sudo systemctl status playkeeper-agent shows why.",
		Err:  err}
}
