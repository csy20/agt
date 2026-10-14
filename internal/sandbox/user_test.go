package sandbox

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDockerUserNeverNegative(t *testing.T) {
	if _, err := dockerUser("linux", -1, -1); err == nil {
		t.Fatal("accepted -1:-1")
	}
	if _, err := dockerUser("darwin", 10, -1); err == nil {
		t.Fatal("accepted a negative gid")
	}
	user, err := dockerUser("windows", -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	if user != sandboxUser || strings.Contains(user, "-1") {
		t.Fatalf("windows user %q", user)
	}
	got, err := dockerUser("linux", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1000:1000" {
		t.Fatalf("linux user %s", got)
	}
}

func TestCurrentDockerUserIsSafe(t *testing.T) {
	user, err := currentDockerUser()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(user, "-1") || user == "" {
		t.Fatalf("user %q", user)
	}
	if runtime.GOOS != "windows" {
		want := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
		if user != want {
			t.Fatalf("user %s want %s", user, want)
		}
	}
}
