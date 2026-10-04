package usage

import (
	"context"
	"errors"
	"time"
)

// PathReach is where the service checks whether a machine's Minecraft port
// answers from the internet.
const PathReach = "/v1/reach"

// The ports a reach check connects back to: those Playkeeper gives servers,
// from the default game port up. No other port can be checked, so neither
// can a server on a port an install's --game-port put outside them.
const (
	ReachPortMin = 25565
	ReachPortMax = 26564
)

// ReachRequest asks the service to connect back to the address the request
// came from, on Port. It names nothing else: no address and no install ID.
type ReachRequest struct {
	Port int `json:"port"`
}

// Check reports whether the service takes r.
func (r ReachRequest) Check() error {
	if r.Port < ReachPortMin || r.Port > ReachPortMax {
		return invalid("port")
	}
	return nil
}

// What a reach check found (ReachAnswer.Result).
const (
	// ReachReachable is a Minecraft server answering its status request.
	ReachReachable = "reachable"
	// ReachRefused is a closed port: the connection was refused.
	ReachRefused = "refused"
	// ReachTimeout is a connection nothing answered in time, as when a
	// firewall drops it.
	ReachTimeout = "timeout"
	// ReachUnreachable is an address the network said can't be reached, as
	// when a firewall rejects the connection.
	ReachUnreachable = "unreachable"
	// ReachNotMinecraft is something else answering on the port.
	ReachNotMinecraft = "not-minecraft"
)

var reachResults = map[string]bool{ReachReachable: true, ReachRefused: true, ReachTimeout: true, ReachUnreachable: true, ReachNotMinecraft: true}

// ReachAnswer is what the service found.
type ReachAnswer struct {
	Result string `json:"result"`
}

// reachTimeout bounds a reach check: the service waits up to 5 seconds for
// the connection, and 5 more for the server's answer.
const reachTimeout = 20 * time.Second

// Reach asks the service whether port answers from the internet on the
// address this machine's requests come from.
func (c Client) Reach(ctx context.Context, port int) (ReachAnswer, error) {
	req := ReachRequest{Port: port}
	if err := req.Check(); err != nil {
		return ReachAnswer{}, err
	}
	var a ReachAnswer
	if err := c.do(ctx, PathReach, req, &a, reachTimeout); err != nil {
		return ReachAnswer{}, err
	}
	if !reachResults[a.Result] {
		return ReachAnswer{}, errors.New("the stats service answered the check with a result Playkeeper doesn't know")
	}
	return a, nil
}
