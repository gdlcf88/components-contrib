package shared

import "sync"

type ErrorCollection struct {
	errors    []error
	mux       sync.Mutex
	ErrNotify chan struct{}
}

func NewErrorCollection() ErrorCollection {
	return ErrorCollection{
		errors:    []error{},
		ErrNotify: make(chan struct{}),
	}
}

func (c *ErrorCollection) Append(e error) {
	c.mux.Lock()
	if len(c.errors) == 0 {
		close(c.ErrNotify)
	}
	c.errors = append(c.errors, e)
	c.mux.Unlock()
}

func (c *ErrorCollection) GetErrors() []error {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.errors
}
