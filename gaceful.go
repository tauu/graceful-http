package gracefulhttp

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// This package is based upon the graceful shutdown example provided by gin:
// https://github.com/gin-gonic/examples/blob/master/graceful-shutdown/graceful-shutdown/notify-without-context/server.go

// Server defines a http server that is gracefully shutdown if the process is
// interrupted or terminated.
type Server struct {
	// The http server controller by this graceful server.
	HttpServer *http.Server
	// The duration which the http server is granted to gracefully shutdown.
	GraceDuration time.Duration
	// The function to run before the http server is shutdown. (optional)
	PreShutdown func()
}

// ServeWithShutdown starts the http server using the Serve function and stops
// it gracefully, when the process receives either an interrupt or terminate
// signal. If starting the http server causes an error, the error is returned
// directly.
func (s *Server) ServeWithShutdown(l net.Listener) error {
	start := func() error { return s.HttpServer.Serve(l) }
	return s.runWithShutdown(start)
}

// ServeTLSWithShutdown starts the http server using the ServeTLS function and
// stops it gracefully, when the process receives either an interrupt or
// terminate signal. If starting the http server causes an error, the error is
// returned directly.
func (s *Server) ServeTLSWithShutdown(l net.Listener, certFile string, keyFile string) error {
	start := func() error { return s.HttpServer.ServeTLS(l, certFile, keyFile) }
	return s.runWithShutdown(start)
}

// ListenAndServeWithShutdown starts the http server using the ListenAndServe
// function and stops it gracefully, when the process receives either an
// interrupt or terminate signal. If starting the http server causes an error,
// the error is returned directly.
func (s *Server) ListenAndServeWithShutdown() error {
	start := func() error { return s.HttpServer.ListenAndServe() }
	return s.runWithShutdown(start)
}

// ListenAndServeTLSWithShutdown starts the http server using the
// ListenAndServeTLS function and stops it gracefully, when the process receives
// either an interrupt or terminate signal. If starting the http server causes
// an error, the error is returned directly.
func (s *Server) ListenAndServeTLSWithShutdown(certFile string, keyFile string) error {
	start := func() error { return s.HttpServer.ListenAndServeTLS(certFile, keyFile) }
	return s.runWithShutdown(start)
}

// runWithShudown starts the http server and stops it gracefully,
// when the process receives either an interrupt or terminate signal.
// If starting the http server causes an error, the error is returned directly.
func (s *Server) runWithShutdown(start func() error) error {
	// Create a channel to signal a startup error.
	startupError := make(chan error, 1)

	// Run the server in a go routine as ListenAndServe is blocking.
	go func() {
		if err := start(); err != nil && err != http.ErrServerClosed {
			startupError <- err
			close(startupError)
		}
	}()

	// Create a channel to catch the interrupt and terminate signals,
	// which are used to stop the process. SIGTERM can never be catch and is
	// therefore not included.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Wait until either a startup error occurs, or the process should quit.
	select {
	case <-quit:
	case err := <-startupError:
		return err
	}

	// Trigger a pre shutdown function.
	if s.PreShutdown != nil {
		s.PreShutdown()
	}

	// If GraceDuration is set, grant the server the specified time to shutdown.
	ctx := context.Background()
	if s.GraceDuration > 0 {
		var cancel context.CancelFunc = nil
		ctx, cancel = context.WithTimeout(ctx, s.GraceDuration)
		defer cancel()
	}
	err := s.HttpServer.Shutdown(ctx)
	return err
}
