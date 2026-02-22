//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/testcontainers/testcontainers-go/modules/compose"
	valkeygo "github.com/valkey-io/valkey-go"
	"github.com/yoseforb/follow-pkg/logger"
)

// Shared test state — set by setupLocal()/setupDocker(), read by all test files.
var (
	apiURL        string
	gatewayURL    string
	valkeyAddress string
)

// Lifecycle handles — used only by setup/teardown.
var (
	composeStack   compose.ComposeStack
	apiProcess     *exec.Cmd
	gatewayProcess *exec.Cmd
)

func initLogger() {
	_ = logger.InitGlobalLogger(
		"follow-integration-tests",
		&logger.LoggingConfig{
			Level:  "debug",
			Format: "console",
			Colors: true,
		},
	)
}

func TestMain(m *testing.M) {
	initLogger()

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
	valkeyAddress = envOrDefault(
		"VALKEY_ADDRESS",
		"localhost:6379",
	)
	apiURL = envOrDefault("API_URL", "http://localhost:8080")
	gatewayURL = envOrDefault(
		"GATEWAY_URL",
		"http://localhost:8090",
	)

	apiPort := portFromURL(apiURL, "8080")
	gatewayPort := portFromURL(gatewayURL, "8090")

	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		log.Error().Err(err).Msg("failed to determine project root")
		os.Exit(1)
	}
	apiDir := filepath.Join(projectRoot, "follow-api")
	gatewayDir := filepath.Join(projectRoot, "follow-image-gateway")

	waitForValkey(valkeyAddress)

	log.Info().
		Str("dir", gatewayDir).
		Str("port", gatewayPort).
		Msg("starting follow-image-gateway")
	gatewayProcess = exec.Command(
		"go", "run", "./cmd/server",
		"-host", "localhost",
		"-port", gatewayPort,
		"-log-level", "debug",
		"-runtime-timeout", "0",
	)
	gatewayProcess.Dir = gatewayDir
	gatewayProcess.Stdout = os.Stdout
	gatewayProcess.Stderr = os.Stderr
	err = gatewayProcess.Start()
	if err != nil {
		log.Error().Err(err).Msg("failed to start follow-image-gateway")
		os.Exit(1)
	}

	log.Info().
		Str("dir", apiDir).
		Str("port", apiPort).
		Msg("starting follow-api")
	apiProcess = exec.Command(
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
	err = apiProcess.Start()
	if err != nil {
		log.Error().Err(err).Msg("failed to start follow-api")
		_ = gatewayProcess.Process.Kill()
		os.Exit(1)
	}

	waitForService(gatewayURL + "/health")
	waitForService(apiURL + "/health")

	log.Info().
		Str("api_url", apiURL).
		Str("gateway_url", gatewayURL).
		Str("valkey", valkeyAddress).
		Msg("local mode setup complete")
}

func setupDocker() {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		log.Error().Err(err).Msg("failed to determine project root")
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
		err := os.Setenv(k, v)
		if err != nil {
			log.Error().Str("key", k).Err(err).Msg(
				"failed to set env override",
			)
			os.Exit(1)
		}
	}

	stack, err := compose.NewDockerCompose(composePath)
	if err != nil {
		log.Error().Err(err).Msg("failed to create compose stack")
		os.Exit(1)
	}
	composeStack = stack

	ctx := context.Background()
	err = composeStack.Up(ctx, compose.Wait(true))
	if err != nil {
		log.Error().Err(err).Msg("failed to start compose stack")
		os.Exit(1)
	}

	valkeyAddress = "localhost:16379"
	apiURL = "http://localhost:18080"
	gatewayURL = "http://localhost:18090"

	log.Info().
		Str("api_url", apiURL).
		Str("gateway_url", gatewayURL).
		Str("valkey", valkeyAddress).
		Msg("docker mode setup complete")
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
	err := composeStack.Down(
		ctx,
		compose.RemoveVolumes(true),
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to tear down compose stack")
	}
}

func killProcess(name string, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	log.Info().
		Str("name", name).
		Int("pid", cmd.Process.Pid).
		Msg("stopping service")
	err := cmd.Process.Signal(syscall.SIGTERM)
	if err != nil {
		log.Warn().
			Str("name", name).
			Err(err).
			Msg("SIGTERM failed, sending SIGKILL")
		_ = cmd.Process.Kill()
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		log.Info().Str("name", name).Msg("service stopped gracefully")
	case <-time.After(5 * time.Second):
		log.Warn().
			Str("name", name).
			Msg("service did not stop in 5s, sending SIGKILL")
		_ = cmd.Process.Kill()
		<-done
	}
}

func waitForService(serviceURL string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(serviceURL)
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			log.Info().Str("url", serviceURL).Msg("service ready")
			return
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(1 * time.Second)
	}
	log.Error().Str("url", serviceURL).Msg("service not reachable after 60s")
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
				log.Info().Str("addr", addr).Msg("valkey ready")
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Error().Str("addr", addr).Msg("valkey not reachable after 30s")
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
