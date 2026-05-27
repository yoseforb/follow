//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var sixDigitCode = regexp.MustCompile(`\b\d{6}\b`)

type mailpitMessageSummary struct {
	ID string `json:"ID"`
}

type mailpitSearchResponse struct {
	Messages []mailpitMessageSummary `json:"messages"`
}

type mailpitFullMessage struct {
	ID   string `json:"ID"`
	Text string `json:"Text"`
	HTML string `json:"HTML"`
}

// uniqueEmail returns a globally unique email address
// suitable for a single test run.
func uniqueEmail() string {
	return fmt.Sprintf(
		"test-%s@follow-test.com",
		uuid.New().String()[:8],
	)
}

// clearMailbox deletes all messages from Mailpit.
func clearMailbox(t *testing.T) {
	t.Helper()

	req, err := http.NewRequest(
		http.MethodDelete,
		mailpitURL+"/api/v1/messages",
		nil,
	)
	require.NoError(t, err,
		"clearMailbox: failed to create request",
	)

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Do(req)
	require.NoError(t, err,
		"clearMailbox: request failed",
	)
	defer resp.Body.Close()
}

// waitForEmail polls Mailpit until at least one email
// addressed to toAddr arrives. Returns the message ID of
// the most recent match.
func waitForEmail(
	t *testing.T,
	toAddr string,
) string {
	t.Helper()

	const (
		pollInterval = 250 * time.Millisecond
		timeout      = 10 * time.Second
	)

	deadline := time.Now().Add(timeout)
	searchURL := mailpitURL + "/api/v1/search?query=" +
		url.QueryEscape("to:"+toAddr)

	client := &http.Client{Timeout: 5 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(searchURL)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		var result mailpitSearchResponse

		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		if err == nil && len(result.Messages) > 0 {
			return result.Messages[0].ID
		}

		time.Sleep(pollInterval)
	}

	t.Fatalf(
		"waitForEmail: no email to %s within %s",
		toAddr, timeout,
	)

	return ""
}

// extractVerificationCode fetches the full message from
// Mailpit and returns the first 6-digit code found in the
// plain-text (or HTML) body.
func extractVerificationCode(
	t *testing.T,
	messageID string,
) string {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(
		mailpitURL + "/api/v1/message/" + messageID,
	)
	require.NoError(t, err,
		"extractVerificationCode: request failed",
	)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"extractVerificationCode: unexpected status",
	)

	var msg mailpitFullMessage

	err = json.NewDecoder(resp.Body).Decode(&msg)
	require.NoError(t, err,
		"extractVerificationCode: decode failed",
	)

	body := msg.Text
	if body == "" {
		body = msg.HTML
	}

	require.NotEmpty(t, body,
		"extractVerificationCode: email body empty",
	)

	code := sixDigitCode.FindString(body)
	require.NotEmpty(t, code,
		"extractVerificationCode: "+
			"no 6-digit code in email body",
	)

	return code
}
