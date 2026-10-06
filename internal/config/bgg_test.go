package config

import "testing"

func TestBGGApplicationTokenFromEnvironment(t *testing.T) {
	t.Setenv("BGG_API_TOKEN", "synthetic-config-token")
	if Load().BGGAPIToken != "synthetic-config-token" {
		t.Fatal("server BGG token was not loaded")
	}
	t.Setenv("BGG_API_TOKEN", "")
	if Load().BGGAPIToken != "" {
		t.Fatal("missing BGG token must remain unset")
	}
}
