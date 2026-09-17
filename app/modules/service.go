package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// StartServer is set by the workers package at init time to avoid an import
// cycle (workers imports modules, but the CLI in modules must start the server).
// The API server (apiPort) and the Web server (webPort) are completely separate
// listeners.
var StartServer func(apiListen, webListen string, apiPort, webPort int) error

// StopServer sends a graceful shutdown request to a running local server.
func StopServer(port int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/control/shutdown", port)
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to connect to server on port %d: %w", port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		fmt.Println("Server shutdown initiated")
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("shutdown request failed: %s", string(body))
}

// ShowServerStatus queries a running local server's status endpoint.
func ShowServerStatus(port int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/control/status", port)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to connect to server on port %d: %w", port, err)
	}
	defer resp.Body.Close()

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fmt.Errorf("failed to parse status: %w", err)
	}
	for _, k := range []string{"status", "name", "version", "api_listen", "web_listen", "uptime", "tokens"} {
		if v, ok := status[k]; ok {
			fmt.Printf("%-12s %v\n", k+":", v)
		}
	}
	return nil
}
