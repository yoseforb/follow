//go:build integration

package integration_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/compose"
	valkeygo "github.com/valkey-io/valkey-go"
)

var (
	apiURL         string
	gatewayURL     string
	valkeyAddress  string
	composeStack   compose.ComposeStack
	apiProcess     *exec.Cmd
	gatewayProcess *exec.Cmd
)

func TestMain(m *testing.M) {
	mode := envOrDefault("INTEGRATION_TEST_MODE", "local")

	switch mode {
	case "docker":
		setupDocker()
	default:
		setupLocal()
	}

	code := m.Run()

	switch mode {
	case "docker":
		teardownDocker()
	default:
		teardownLocal()
	}

	os.Exit(code)
}

func setupLocal() {
	valkeyAddress = envOrDefault("VALKEY_ADDRESS", "localhost:6379")
	apiURL = envOrDefault("API_URL", "http://localhost:8080")
	gatewayURL = envOrDefault("GATEWAY_URL", "http://localhost:8090")

	apiPort := portFromURL(apiURL, "8080")
	gatewayPort := portFromURL(gatewayURL, "8090")

	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		slog.Error(
			"failed to determine project root",
			"error", err,
		)
		os.Exit(1)
	}
	apiDir := filepath.Join(projectRoot, "follow-api")
	gatewayDir := filepath.Join(projectRoot, "follow-image-gateway")

	waitForValkey(valkeyAddress)

	slog.Info(
		"starting follow-image-gateway",
		"dir", gatewayDir,
		"port", gatewayPort,
	)
	gatewayProcess = exec.Command( //nolint:gosec
		"go", "run", "./cmd/server",
		"-host", "localhost",
		"-port", gatewayPort,
		"-log-level", "debug",
		"-runtime-timeout", "0",
	)
	gatewayProcess.Dir = gatewayDir
	gatewayProcess.Stdout = os.Stdout
	gatewayProcess.Stderr = os.Stderr
	if err := gatewayProcess.Start(); err != nil {
		slog.Error(
			"failed to start follow-image-gateway",
			"error", err,
		)
		os.Exit(1)
	}

	slog.Info(
		"starting follow-api",
		"dir", apiDir,
		"port", apiPort,
	)
	apiProcess = exec.Command( //nolint:gosec
		"go", "run", "./cmd/server",
		"-host", "localhost",
		"-port", apiPort,
		"-log-level", "debug",
		"-runtime-timeout", "0",
	)
	apiProcess.Dir = apiDir
	apiProcess.Env = append(
		os.Environ(),
		"GATEWAY_BASE_URL=http://localhost:"+gatewayPort,
	)
	apiProcess.Stdout = os.Stdout
	apiProcess.Stderr = os.Stderr
	if err := apiProcess.Start(); err != nil {
		slog.Error(
			"failed to start follow-api",
			"error", err,
		)
		_ = gatewayProcess.Process.Kill()
		os.Exit(1)
	}

	waitForService(gatewayURL + "/health/")
	waitForService(apiURL + "/health/")

	slog.Info(
		"local mode setup complete",
		"api_url", apiURL,
		"gateway_url", gatewayURL,
		"valkey", valkeyAddress,
	)
}

func setupDocker() {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		slog.Error(
			"failed to determine project root",
			"error", err,
		)
		os.Exit(1)
	}

	composePath := filepath.Join(projectRoot, "docker-compose.yml")

	envOverrides := map[string]string{
		"POSTGRES_HOST_PORT":      "15432",
		"VALKEY_HOST_PORT":        "16379",
		"MINIO_HOST_PORT":         "19000",
		"MINIO_CONSOLE_HOST_PORT": "19001",
		"API_HOST_PORT":           "18080",
		"GATEWAY_HOST_PORT":       "18090",
		"POSTGRES_CONTAINER_NAME": "follow-postgres-test",
		"VALKEY_CONTAINER_NAME":   "follow-valkey-test",
		"MINIO_CONTAINER_NAME":    "follow-minio-test",
		"API_CONTAINER_NAME":      "follow-api-test",
		"GATEWAY_CONTAINER_NAME":  "follow-image-gateway-test",
		"NETWORK_NAME":            "follow-internal-test",
	}

	for k, v := range envOverrides {
		if err := os.Setenv(k, v); err != nil {
			slog.Error(
				"failed to set env override",
				"key", k,
				"error", err,
			)
			os.Exit(1)
		}
	}

	stack, err := compose.NewDockerCompose(composePath)
	if err != nil {
		slog.Error(
			"failed to create compose stack",
			"error", err,
		)
		os.Exit(1)
	}
	composeStack = stack

	ctx := context.Background()
	if err := composeStack.Up(ctx, compose.Wait(true)); err != nil {
		slog.Error(
			"failed to start compose stack",
			"error", err,
		)
		os.Exit(1)
	}

	valkeyAddress = "localhost:16379"
	apiURL = "http://localhost:18080"
	gatewayURL = "http://localhost:18090"

	slog.Info(
		"docker mode setup complete",
		"api_url", apiURL,
		"gateway_url", gatewayURL,
		"valkey", valkeyAddress,
	)
}

func teardownLocal() {
	killProcess("follow-api", apiProcess)
	killProcess("follow-image-gateway", gatewayProcess)
}

func teardownDocker() {
	if composeStack == nil {
		return
	}
	ctx := context.Background()
	if err := composeStack.Down(
		ctx,
		compose.RemoveVolumes(true),
	); err != nil {
		slog.Error("failed to tear down compose stack", "error", err)
	}
}

func killProcess(name string, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	slog.Info(
		"stopping service",
		"name", name,
		"pid", cmd.Process.Pid,
	)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		slog.Warn(
			"SIGTERM failed, sending SIGKILL",
			"name", name,
			"error", err,
		)
		_ = cmd.Process.Kill()
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		slog.Info("service stopped gracefully", "name", name)
	case <-time.After(5 * time.Second):
		slog.Warn(
			"service did not stop in 5s, sending SIGKILL",
			"name", name,
		)
		_ = cmd.Process.Kill()
		<-done
	}
}

func waitForService(serviceURL string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(serviceURL) //nolint:noctx
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			slog.Info("service ready", "url", serviceURL)
			return
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(1 * time.Second)
	}
	slog.Error("service not reachable after 60s", "url", serviceURL)
	os.Exit(1)
}

func waitForValkey(addr string) {
	cfg := valkeygo.ClientOption{
		InitAddress:  []string{addr},
		DisableCache: true,
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		client, err := valkeygo.NewClient(cfg)
		if err == nil {
			err = client.Do(
				context.Background(),
				client.B().Ping().Build(),
			).Error()
			client.Close()
			if err == nil {
				slog.Info("valkey ready", "addr", addr)
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	slog.Error("valkey not reachable after 30s", "addr", addr)
	os.Exit(1)
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func portFromURL(rawURL, defaultPort string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Port() == "" {
		return defaultPort
	}
	return u.Port()
}
