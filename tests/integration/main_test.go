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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/joho/godotenv"
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
	apiURL = envOrDefault("API_URL", "http://localhost:8085")
	gatewayURL = envOrDefault(
		"GATEWAY_URL",
		"http://localhost:8095",
	)

	apiPort := portFromURL(apiURL, "8085")
	gatewayPort := portFromURL(gatewayURL, "8095")

	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		log.Error().Err(err).Msg("failed to determine project root")
		os.Exit(1)
	}
	apiDir := filepath.Join(projectRoot, "follow-api")
	gatewayDir := filepath.Join(projectRoot, "follow-image-gateway")

	waitForValkey(valkeyAddress)
	cleanValkeyStreams(valkeyAddress)

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
	gatewayProcess.Env = gatewayEnv()
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
		"RATE_LIMIT_ENABLED=false",
		"REAPER_SCAN_INTERVAL=1s",
		"REAPER_STALE_THRESHOLD=2s",
		"RECLAIMER_IDLE_TIMEOUT=5s",
		"RECLAIMER_SCAN_INTERVAL=2s",
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
	// Test-only override merged on top of the root compose file.
	// Adds follow-api env vars (RATE_LIMIT_ENABLED=false, reclaimer
	// tuning) that mirror setupLocal()'s subprocess env, without
	// touching the base compose used by the dev stack.
	composeTestOverride := "docker-compose.test.yml"

	// Optional extra compose overrides, e.g. memory-limit
	// profiles for stress testing. Set COMPOSE_EXTRA_FILES to a
	// comma-separated list of paths (relative to project root).
	composeFiles := []string{composePath, composeTestOverride}
	if extra := os.Getenv("COMPOSE_EXTRA_FILES"); extra != "" {
		for f := range strings.SplitSeq(extra, ",") {
			f = strings.TrimSpace(f)
			abs := filepath.Join(projectRoot, f)
			composeFiles = append(composeFiles, abs)
			log.Info().
				Str("file", abs).
				Msg("docker: extra compose file added")
		}
	}

	// Load tests/integration/.env into the process environment.
	// This is the single source of truth for docker-mode config:
	// test-only credentials, port overrides (25xxx/26xxx/28xxx/29xxx
	// to avoid collision with the dev stack), container/network names
	// suffixed with -test, and HOST_IP=localhost so the follow-api
	// emits presigned URLs that the test binary can actually reach.
	// testcontainers-go does not auto-load .env, so we load it here
	// and rely on composeStack.WithOsEnv() below to forward the
	// values into compose variable substitution.
	envMap, err := godotenv.Read(".env")
	if err != nil {
		log.Error().Err(err).Msg(
			"failed to read tests/integration/.env",
		)
		os.Exit(1)
	}
	for k, v := range envMap {
		err = os.Setenv(k, v)
		if err != nil {
			log.Error().Str("key", k).Err(err).Msg(
				"failed to set env from .env",
			)
			os.Exit(1)
		}
	}

	// Use a stable StackIdentifier so every run shares the same
	// compose project name. Without this, tc-go generates a fresh
	// UUID per NewDockerCompose call and containers from a crashed
	// previous run carry the *previous* project label — invisible
	// to the current run's Down(), so defensive cleanup cannot find
	// them. With a pinned identifier, run N+1's Down() sees run N's
	// leftovers via the shared compose project label and wipes them.
	stack, err := compose.NewDockerComposeWith(
		compose.WithStackFiles(composeFiles...),
		compose.StackIdentifier("follow-integration-test"),
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to create compose stack")
		os.Exit(1)
	}
	composeStack = stack.WithOsEnv()

	ctx := context.Background()

	// Defensive teardown before Up: if a previous run crashed without
	// calling teardownDocker, stale follow-*-test containers and/or
	// named volumes (postgres_data, valkey_data, minio_data) will
	// block compose from creating the fresh set, and stale data in
	// the named volumes would poison the next run. Down() with
	// RemoveVolumes wipes both. Errors are logged but not fatal —
	// "nothing to tear down" is the expected state on a clean run.
	err = composeStack.Down(ctx, compose.RemoveVolumes(true))
	if err != nil {
		log.Warn().Err(err).Msg(
			"pre-run compose Down returned an error " +
				"(usually safe: nothing to tear down)",
		)
	}

	// WithOsEnv (applied above on stack creation) ensures
	// testcontainers evaluates compose variable substitutions
	// (e.g. ${NETWORK_NAME:-follow-internal}) using the current
	// process environment, which now contains everything from
	// tests/integration/.env. Without it, the compose-go library
	// falls back to defaults and tries to manage the dev-stack
	// network by mistake.
	err = composeStack.Up(ctx, compose.Wait(true))
	if err != nil {
		log.Error().Err(err).Msg("failed to start compose stack")
		os.Exit(1)
	}

	// Host-side URLs are built from HOST_IP + *_HOST_PORT in .env so
	// this code stays in sync with the compose mappings without
	// hard-coding values in two places. HOST_IP is normally "localhost"
	// in the test .env but can be pointed at a LAN IP (e.g. for remote
	// debugging from another machine) by editing .env alone.
	hostIP := envMap["HOST_IP"]
	valkeyAddress = hostIP + ":" + envMap["VALKEY_HOST_PORT"]
	apiURL = "http://" + hostIP + ":" + envMap["API_HOST_PORT"]
	gatewayURL = "http://" + hostIP + ":" + envMap["GATEWAY_HOST_PORT"]

	// Match setupLocal: wipe any stale image:result / image:result:dlq
	// streams so the API consumer group starts with a fresh watermark.
	// With the defensive Down above, volumes are already wiped on a
	// crash-recovered run, but calling this here keeps docker mode in
	// parity with local mode and provides cheap insurance.
	cleanValkeyStreams(valkeyAddress)

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

// cleanValkeyStreams deletes the image:result stream (and its
// consumer group) so the API consumer starts with a fresh group
// on each test run. Without this, stale consumer group state
// from a previous run can cause the consumer to skip messages
// whose IDs fall below the group's last-delivered watermark.
func cleanValkeyStreams(addr string) {
	cfg := valkeygo.ClientOption{
		InitAddress:  []string{addr},
		DisableCache: true,
	}
	client, err := valkeygo.NewClient(cfg)
	if err != nil {
		log.Warn().Err(err).
			Msg("failed to connect to valkey for stream cleanup")
		return
	}
	defer client.Close()

	ctx := context.Background()
	streams := []string{"image:result", "image:result:dlq"}
	for _, key := range streams {
		err = client.Do(
			ctx,
			client.B().Del().Key(key).Build(),
		).Error()
		if err != nil {
			log.Warn().Err(err).Str("key", key).
				Msg("failed to delete valkey stream")
		} else {
			log.Info().Str("key", key).
				Msg("deleted valkey stream for clean test run")
		}
	}
}

// gatewayEnv builds the environment for the gateway subprocess
// in local mode. It forwards GATEWAY_* env vars to the gateway
// process without affecting the test binary or the API:
//
//	GATEWAY_GOMEMLIMIT  → GOMEMLIMIT  (Go GC memory target)
//	GATEWAY_GODEBUG     → GODEBUG     (e.g. gctrace=1)
//	GATEWAY_MALLOC_TRIM → MALLOC_TRIM_THRESHOLD_ (glibc tuning)
func gatewayEnv() []string {
	env := os.Environ()

	forwards := []struct {
		src string
		dst string
	}{
		{"GATEWAY_GOMEMLIMIT", "GOMEMLIMIT"},
		{"GATEWAY_GODEBUG", "GODEBUG"},
		{
			"GATEWAY_MALLOC_TRIM",
			"MALLOC_TRIM_THRESHOLD_",
		},
	}

	for _, f := range forwards {
		if v := os.Getenv(f.src); v != "" {
			env = append(env, f.dst+"="+v)
			log.Info().
				Str(f.dst, v).
				Msg("gateway: env override set")
		}
	}

	return env
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
