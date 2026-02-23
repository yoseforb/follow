//go:build integration

package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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
	composeStack     compose.ComposeStack
	apiProcess       *exec.Cmd
	gatewayProcess   *exec.Cmd
	apiDrainWait     func()
	gatewayDrainWait func()
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
	// Setpgid places the process in its own process group. When we later
	// signal -pgid, both the `go run` parent and the compiled server
	// grandchild receive the signal, so no orphaned process holds the test
	// binary's I/O open after cleanup.
	gatewayProcess.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	gatewayDrainWait = pipeOutput(gatewayProcess)
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
	// Setpgid places the process in its own process group. When we later
	// signal -pgid, both the `go run` parent and the compiled server
	// grandchild receive the signal, so no orphaned process holds the test
	// binary's I/O open after cleanup.
	apiProcess.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	apiDrainWait = pipeOutput(apiProcess)
	err = apiProcess.Start()
	if err != nil {
		log.Error().Err(err).Msg("failed to start follow-api")
		killProcessGroup(
			"follow-image-gateway",
			gatewayProcess,
			gatewayDrainWait,
		)
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
	killProcessGroup("follow-api", apiProcess, apiDrainWait)
	killProcessGroup(
		"follow-image-gateway",
		gatewayProcess,
		gatewayDrainWait,
	)
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

// pipeOutput attaches pipes to cmd's stdout and stderr and starts goroutines
// that copy output to os.Stdout/os.Stderr. Using pipes instead of assigning
// os.Stdout/os.Stderr directly prevents the file descriptors from being
// inherited by grandchild processes spawned by `go run`, so orphaned
// grandchildren cannot keep the test binary's I/O open after cleanup.
//
// pipeOutput returns a drain function that blocks until both copy goroutines
// have finished. The caller must invoke this function after cmd.Wait()
// returns to guarantee all buffered output is flushed before os.Exit.
//
// Must be called before cmd.Start().
func pipeOutput(cmd *exec.Cmd) func() {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Error().Err(err).Msg("failed to create stdout pipe")
		os.Exit(1)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		log.Error().Err(err).Msg("failed to create stderr pipe")
		os.Exit(1)
	}

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(os.Stdout, stdoutPipe)
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(os.Stderr, stderrPipe)
	}()

	return wg.Wait
}

// killProcessGroup sends SIGTERM to the entire process group of cmd
// (negative pgid), waits up to 5 seconds for a graceful exit, then sends
// SIGKILL to the group if it has not stopped. Signaling the whole group
// ensures that grandchild processes created by `go run` (the compiled server
// binary) are also terminated. After the process exits, drain is called to
// block until all pipe-copy goroutines have flushed their output.
func killProcessGroup(name string, cmd *exec.Cmd, drain func()) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	pgid := cmd.Process.Pid

	log.Info().
		Str("name", name).
		Int("pid", pgid).
		Msg("stopping service")

	// Signal the entire process group (negative pid = process group id).
	err := syscall.Kill(-pgid, syscall.SIGTERM)
	if err != nil {
		log.Warn().
			Str("name", name).
			Err(err).
			Msg("SIGTERM to process group failed, killing direct process")
		_ = cmd.Process.Kill()

		if drain != nil {
			drain()
		}

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
			Msg("service did not stop in 5s, killing process group")
		// Kill the entire group to ensure grandchildren are also terminated.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
	}

	// Wait for the pipe-copy goroutines to finish draining. cmd.Wait()
	// closes the pipe write ends, causing the goroutines to receive EOF and
	// return. Without this wait, output written just before process exit
	// could be lost, and — more importantly — we ensure the goroutines have
	// fully exited before os.Exit is called by TestMain.
	if drain != nil {
		drain()
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
