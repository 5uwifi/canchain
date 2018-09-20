package rpc

import "testing"

func TestWSGetConfigNoAuth(t *testing.T) {
	config, err := wsGetConfig("ws://example.com:1234", "")
	if err != nil {
		t.Logf("wsGetConfig failed: %s", err)
		t.Fail()
		return
	}
	if config.Location.User != nil {
		t.Log("User should have been stripped from the URL")
		t.Fail()
	}
	if config.Location.Hostname() != "example.com" ||
		config.Location.Port() != "1234" || config.Location.Scheme != "ws" {
		t.Logf("Unexpected URL: %s", config.Location)
		t.Fail()
	}
}

func TestWSGetConfigWithBasicAuth(t *testing.T) {
	config, err := wsGetConfig("wss://testuser:test-PASS_01@example.com:1234", "")
	if err != nil {
		t.Logf("wsGetConfig failed: %s", err)
		t.Fail()
		return
	}
	if config.Location.User != nil {
		t.Log("User should have been stripped from the URL")
		t.Fail()
	}
	if config.Header.Get("Authorization") != "Basic dGVzdHVzZXI6dGVzdC1QQVNTXzAx" {
		t.Log("Basic auth header is incorrect")
		t.Fail()
	}
}
