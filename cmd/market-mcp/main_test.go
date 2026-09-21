package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

func TestLoadConfigRejectsPublicListeners(t *testing.T) {
	t.Setenv("MARKET_MCP_HTTP_ADDR", "0.0.0.0:8082")
	_, err := loadConfig()
	assert.ErrorContains(t, err, "loopback")
}

func TestLoadConfigRejectsInvalidResultLimit(t *testing.T) {
	t.Setenv("MARKET_MCP_MAX_RESULT_BYTES", "100")
	_, err := loadConfig()
	assert.ErrorContains(t, err, "MAX_RESULT_BYTES")
}

func TestStreamableHTTPEndpoint(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1"}, nil)
	handler := httpHandler(server)

	other := httptest.NewRequestWithContext(t.Context(), "GET", "http://127.0.0.1/other", nil)
	otherResponse := httptest.NewRecorder()
	handler.ServeHTTP(otherResponse, other)
	assert.Equal(t, 404, otherResponse.Code)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "http://127.0.0.1/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	assert.Equal(t, 200, response.Code)
	assert.Contains(t, response.Body.String(), "test-server")
}
