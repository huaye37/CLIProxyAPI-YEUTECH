//go:build !unix

package poollease

import "errors"

// Unsupported platforms must never silently allow concurrent refresh.
type Lease struct{}

func New(string) *Lease                  { return &Lease{} }
func (*Lease) TryAcquire() (bool, error) { return false, errors.New("pool refresh lease unsupported") }
func (*Lease) Close() error              { return nil }
