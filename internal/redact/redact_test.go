package redact

import "strings"

import "testing"

func TestTextHidesSecretsAndKeepsCode(t *testing.T) {
	src := strings.Join([]string{
		"func charge() int { return 1 }",
		"token := config",
		"password=hunter2hunter",
		`api_key: "sk-test-secret-value"`,
		"Authorization: Bearer abcdefghijklmnop",
		"key AKIAIOSFODNN7EXAMPLE end",
		"-----BEGIN PRIVATE KEY-----",
		"not-a-real-key",
		"-----END PRIVATE KEY-----",
		"-----BEGIN PUBLIC KEY-----",
		"leave-public",
		"-----END PUBLIC KEY-----",
	}, "\n")
	out := Text(src)
	for _, secret := range []string{"hunter2hunter", "sk-test-secret-value", "abcdefghijklmnop", "AKIAIOSFODNN7EXAMPLE", "not-a-real-key"} {
		if strings.Contains(out, secret) {
			t.Fatalf("still visible %q\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "func charge() int { return 1 }") {
		t.Fatalf("code removed\n%s", out)
	}
	if !strings.Contains(out, "token := config") {
		t.Fatalf("assignment rewritten\n%s", out)
	}
	if !strings.Contains(out, "leave-public") {
		t.Fatalf("public key removed\n%s", out)
	}
}
