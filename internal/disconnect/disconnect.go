package disconnect

import "sync"

// Disconnector is a channel that can be closed once.
type Disconnector struct {
	ch   chan bool // channel to close
	once sync.Once // ensures Trigger is idempotent
}

// NewDisconnector creates a new Disconnector.
func NewDisconnector() *Disconnector { return &Disconnector{ch: make(chan bool)} }

// Trigger closes the disconnector channel.
func (d *Disconnector) Trigger() { d.once.Do(func() { close(d.ch) }) }

// Done returns a channel to be used in select statements to
// check for the disconnector status.
func (d *Disconnector) Done() <-chan bool { return d.ch }
