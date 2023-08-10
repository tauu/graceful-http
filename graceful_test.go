package gracefulhttp

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tj/assert"
)

func newHttpTest(t *testing.T, server *Server, handler http.Handler) (chan error, *http.Client, string, error) {
	t.Helper()
	// Create a listner on any available port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, "", err
	}
	t.Cleanup(func() { listener.Close() })
	// Start a server using this listener.
	httpServer := &http.Server{
		Handler: handler,
	}
	server.HttpServer = httpServer
	// Configure a client to connect to the server.
	client := &http.Client{Transport: &http.Transport{}}
	// Assemble the base url for accessing the server.
	addr := listener.Addr().String()
	url := fmt.Sprintf("http://%s", addr)
	// Start the test server.
	errServer := make(chan error, 1)
	go func() {
		err := server.ServeWithShutdown(listener)
		errServer <- err
		close(errServer)
	}()
	if err != nil {
		return nil, nil, "", err
	}
	return errServer, client, url, nil
}

// testHandle responds with "pong" to all http requests.
func testHandle(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pong"))
}

// testSlowHandle responds very slowly to requests to /slow.
func testSlowHandle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/slow" {
		time.Sleep(5 * time.Second)
		w.Write([]byte("slowpong"))
	}
	w.Write([]byte("pong"))
}

// TestStartError checks if an error occuring during startup is returned.
func TestStartError(t *testing.T) {
	// Create a http server with an invalid port number.
	httpServer := &http.Server{
		Addr: "127.0.0.1:-1",
	}
	gracefulServer := &Server{
		HttpServer: httpServer,
	}
	// Start the server.
	err := gracefulServer.ListenAndServeWithShutdown()
	assert.Error(t, err, "running a http server with an invalid port number should cause an error")
}

// TestShutDown verifies that a SIGINT signal sent to the process will
// gracefully shutdown the http server.
func TestShutdown(t *testing.T) {
	errServer, client, url, err := newHttpTest(t, &Server{}, http.HandlerFunc(testHandle))
	assert.NoError(t, err, "creating a test http server should cause an error")
	// Verify that the client can access the server.
	assertHealth(t, true, client, url)
	// Send a sigint signal to gracefull shutdown the server.
	err = gracefulTestShutdown()
	assert.NoError(t, err, "sending SIGINT to this process should not cause an error")
	// Wait until the shutdown is complete.
	err, timedOut := waitForError(errServer, 2*time.Second)
	assert.False(t, timedOut, "waiting for the server to shutdown should not timeout")
	assert.NoError(t, err, "gracefully shutting down the test server should not cause an error")
	// Running the same request now should cause an error as the server was shutdown.
	assertHealth(t, false, client, url)
}

// TestPreShutdown checks that the pre shutdown function is called before the
// the http server is shut down.
func TestPreShutdown(t *testing.T) {
	preShutDownCalled := false
	server := &Server{
		PreShutdown: func() { preShutDownCalled = true },
	}
	errServer, client, url, err := newHttpTest(t, server, http.HandlerFunc(testHandle))
	assert.NoError(t, err, "creating a test http server should cause an error")
	// Verify that the client can access the server.
	assertHealth(t, true, client, url)
	assert.False(t, preShutDownCalled, "the PreShutdown function should not be called before the shutdown was triggered")
	// Send a sigint signal to gracefull shutdown the server.
	err = gracefulTestShutdown()
	assert.NoError(t, err, "sending SIGINT to this process should not cause an error")
	// Wait until the shutdown is complete.
	err, timedOut := waitForError(errServer, 2*time.Second)
	assert.False(t, timedOut, "waiting for the server to shutdown should not timeout")
	assert.NoError(t, err, "gracefully shutting down the test server should not cause an error")
	// Running the same request now should cause an error as the server was shutdown.
	assertHealth(t, false, client, url)
	assert.True(t, preShutDownCalled, "the PreShutdown function should have been called after the shutdown is done")
}

// TestGracePeriod checks that grace duration timeout forces a shutdown after
// timeout is reached.
func TestGracePeriod(t *testing.T) {
	server := &Server{
		GraceDuration: 2 * time.Second,
	}
	errServer, client, url, err := newHttpTest(t, server, http.HandlerFunc(testSlowHandle))
	assert.NoError(t, err, "creating a test http server should cause an error")
	// Verify that the client can access the server.
	assertHealth(t, true, client, url)
	// Run a request that will tigger a slow response which takes longer than
	// the grace duration.
	req, err := http.NewRequest(http.MethodGet, url+"/slow", bytes.NewBufferString("ping"))
	require.NoError(t, err, "creating a test request should not cause an error")
	go func() {
		client.Do(req)
	}()
	// Sleep for a short time to ensure that request was sent.
	time.Sleep(200 * time.Millisecond)
	// Send a sigint signal to gracefull shutdown the server.
	err = gracefulTestShutdown()
	assert.NoError(t, err, "sending SIGINT to this process should not cause an error")
	// Waiting for a shorter time than the grace duration should cause a timeout,
	// because the slow request will not complete before the full grace duration.
	_, timedOut := waitForError(errServer, server.GraceDuration/2)
	assert.True(t, timedOut, "waiting for the server to shutdown after half the grace duration should timeout as the slow request will keep the server alive")
	// Waiting until the full grace duration has elapsed should yield an error as
	// the shudown could not be done gracefully.
	err, timedOut = waitForError(errServer, server.GraceDuration)
	assert.False(t, timedOut, "waiting for the server to shutdown after the full grace duration should never timeout")
	assert.Error(t, err, "gracefully shutting down the test server should cause an error if a slow request takes longer than the grace duration")
	// Running the same request now should cause an error as the server was shutdown.
	assertHealth(t, false, client, url)
}

// waitForError returns the error received from the channel if it it can be read
// before the timeout is reached. timedOut is true if and only if the timeout
// was reached before a value could be read from the channel.
func waitForError(c chan error, timeout time.Duration) (err error, timedOut bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		timedOut = true
		return
	case err = <-c:
		return
	}
}

// assertHealth checks that the health status of test http server is as expected.
func assertHealth(t *testing.T, expected bool, client *http.Client, url string) {
	// Create a http request and execute it.
	req, err := http.NewRequest(http.MethodGet, url, bytes.NewBufferString("ping"))
	require.NoError(t, err, "creating a test request should not cause an error")
	resp, err := client.Do(req)
	if expected {
		assert.NoError(t, err, "sending the test request should not cause an error, if the test http server is expected to be available")
		assert.Equal(t, http.StatusOK, resp.StatusCode, "the test http server should always send an OK status code when expected to be available")
	} else {
		assert.Error(t, err, "the test http server is expected to be not available but responded anyway")
	}
}

// gracefullTestShutdown shuts down the test http server gracefuly by sending
// a signal to the process.
func gracefulTestShutdown() error {
	// Send a sigint signal to gracefull shutdown the server.
	return syscall.Kill(syscall.Getpid(), syscall.SIGINT)
}
