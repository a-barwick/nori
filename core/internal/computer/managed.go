package computer

import (
	"context"
	"fmt"
	"time"
)

// State is the power state of a computer that a Host reports.
type State int

const (
	// Stopped means the host has fully stopped. Nothing is running or flushing.
	Stopped State = iota + 1
	// Stopping means a stop is in progress. The guest may still be flushing to disk.
	Stopping
	// Starting means the computer is powering on or booting.
	Starting
	// Running means the computer is powered on. The guest RPC may not answer yet.
	Running
)

// Host is what an adapter supplies when it can power the computer on and
// observe its state. Managed turns a Host into a Provider.
type Host interface {
	// State reports the current power state. A state with no equivalent above,
	// such as a suspended or repairing machine, is returned as an error and ends
	// the connection attempt.
	State(ctx context.Context) (State, error)
	// Start powers on a Stopped computer. Starting one that another caller has
	// already started is not an error.
	Start(ctx context.Context) error
	// Dial establishes the transport to the guest RPC of a Running computer.
	// It does not check health. The transport must outlive ctx: it carries the
	// client until the client is closed.
	Dial(ctx context.Context) (Connection, error)
}

// readyTimeout bounds a whole connection attempt. An idle stop may already be
// flushing the guest when a new task arrives, so the attempt waits for that
// stop to finish before it starts the computer. No tool has been sent yet.
const readyTimeout = 180 * time.Second

// Managed returns a Provider that drives a Host through its lifecycle. open runs
// once per connection attempt with that attempt's context, so credential and
// configuration failures surface when a process first connects, not at startup.
func Managed(open func(ctx context.Context) (Host, error)) Provider {
	return managed(open)
}

type managed func(ctx context.Context) (Host, error)

func (m managed) Open(ctx context.Context, run string) (*Client, error) {
	return m.connect(ctx, run, true)
}

func (m managed) Attach(ctx context.Context, run string) (*Client, error) {
	return m.connect(ctx, run, false)
}

func (m managed) connect(ctx context.Context, run string, allowStart bool) (*Client, error) {
	ctx, readyCancel := context.WithTimeout(ctx, readyTimeout)
	defer readyCancel()
	host, err := m(ctx)
	if err != nil {
		return nil, err
	}
	var client *Client
	defer func() {
		if client != nil {
			client.Close()
		}
	}()
	startIssued := false
	for {
		state, err := host.State(ctx)
		if err != nil {
			return nil, err
		}
		if state != Running && client != nil {
			client.Close()
			client = nil
		}
		switch state {
		case Stopped:
			if !allowStart {
				return nil, ErrStopped
			}
			if !startIssued {
				if err = host.Start(ctx); err != nil {
					return nil, err
				}
				startIssued = true
			}
		case Stopping:
			// Cleanup may only claim stopped after the host reports Stopped.
			startIssued = false
		case Starting:
		case Running:
			startIssued = false
			if client != nil {
				select {
				case <-client.conn.Done:
					client.Close()
					client = nil
				default:
				}
			}
			if client == nil {
				conn, err := host.Dial(ctx)
				if err != nil {
					return nil, err
				}
				client = NewClient(conn, run)
			}
			if _, err := client.Probe(ctx); err == nil {
				ready := client
				client = nil
				return ready, nil
			}
		default:
			return nil, fmt.Errorf("computer unavailable (state %d)", state)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
