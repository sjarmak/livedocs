// Package sample provides sample functions for testing.
package sample

import (
	"fmt"
	"os"
)

// Greet returns a greeting string for the given name.
func Greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

// Server handles HTTP requests.
type Server struct {
	Port int
}

// Start starts the server on the configured port.
func (s *Server) Start() error {
	_ = os.Getenv("PORT")
	return nil
}

var defaultTimeout = 30
