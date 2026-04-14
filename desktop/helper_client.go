package main

import (
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"time"
)

// HelperClient communicates with the privileged helper over IPC.
type HelperClient struct {
	socketPath string
	timeout    time.Duration
}

// NewHelperClient creates a client that connects to the privileged helper.
func NewHelperClient() *HelperClient {
	return &HelperClient{
		socketPath: helperSocketPath(),
		timeout:    30 * time.Second,
	}
}

// SendCommand sends a command to the helper and returns the response.
// It connects, sends JSON, reads the JSON response, and disconnects.
// Auto-retries the connection once if the first attempt fails.
func (c *HelperClient) SendCommand(action string, args map[string]string) (HelperResponse, error) {
	resp, err := c.sendOnce(action, args)
	if err != nil {
		// Retry once after a short pause (helper may be restarting)
		time.Sleep(500 * time.Millisecond)
		resp, err = c.sendOnce(action, args)
		if err != nil {
			return HelperResponse{}, fmt.Errorf("helper communication failed: %w", err)
		}
	}
	return resp, nil
}

// sendOnce performs a single command send/receive cycle.
func (c *HelperClient) sendOnce(action string, args map[string]string) (HelperResponse, error) {
	conn, err := c.dial()
	if err != nil {
		return HelperResponse{}, fmt.Errorf("failed to connect to helper: %w", err)
	}
	defer conn.Close()

	// Build command
	cmd := HelperCommand{
		Action: action,
	}

	// Extract well-known fields from args
	if args != nil {
		if v, ok := args["protocol"]; ok {
			cmd.Protocol = v
		}
		if v, ok := args["config_path"]; ok {
			cmd.ConfigPath = v
		}
		if v, ok := args["interface"]; ok {
			cmd.Interface = v
		}
		// Pass remaining args
		cmd.Args = args
	}

	// Send command
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(cmd); err != nil {
		return HelperResponse{}, fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	conn.SetReadDeadline(time.Now().Add(c.timeout))
	var resp HelperResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return HelperResponse{}, fmt.Errorf("failed to read response: %w", err)
	}

	return resp, nil
}

// dial connects to the helper socket/pipe.
func (c *HelperClient) dial() (net.Conn, error) {
	network := "unix"
	if runtime.GOOS == "windows" {
		network = "unix" // Go supports Unix sockets on Windows too
	}

	conn, err := net.DialTimeout(network, c.socketPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// IsHelperReachable checks if the helper is responding to commands.
func (c *HelperClient) IsHelperReachable() bool {
	conn, err := c.dial()
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
