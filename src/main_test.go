package main

import (
	"net/http"
	"testing"
)

func TestGetEnvBoolDefault(t *testing.T) {
	t.Setenv("BOOL_TRUE", "true")
	t.Setenv("BOOL_FALSE", "0")
	t.Setenv("BOOL_INVALID", "maybe")

	if !getEnvBoolDefault("BOOL_TRUE", false) {
		t.Fatal("expected true env value")
	}
	if getEnvBoolDefault("BOOL_FALSE", true) {
		t.Fatal("expected false env value")
	}
	if !getEnvBoolDefault("BOOL_MISSING", true) {
		t.Fatal("expected missing env to use default")
	}
	if !getEnvBoolDefault("BOOL_INVALID", true) {
		t.Fatal("expected invalid env to use default")
	}
}

func TestAuthorizedRequest(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer expected")

	if !authorizedRequest(req, "expected") {
		t.Fatal("expected bearer token to authorize")
	}
	if authorizedRequest(req, "other") {
		t.Fatal("wrong bearer token authorized")
	}

	req.Header.Del("Authorization")
	req.Header.Set("X-MCP-Token", "expected")
	if !authorizedRequest(req, "expected") {
		t.Fatal("expected X-MCP-Token to authorize")
	}
}
