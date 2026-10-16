package sandbox

import "testing"

func TestOwnsRequiresNamespaceAndRun(t *testing.T) {
	labels := map[string]string{"agt-namespace": "nsabcdef", "agt-run": "run_1"}
	if !Owns("nsabcdef", labels, "run_1") {
		t.Fatal("owned container was refused")
	}
	if Owns("other", labels, "run_1") || Owns("nsabcdef", labels, "run_2") || Owns("", labels, "run_1") {
		t.Fatal("foreign container was accepted")
	}
	if !Owns("default", map[string]string{"agt-namespace": "default", "agt-run": "run_1"}, "run_1") {
		t.Fatal("default namespace was refused")
	}
	if Owns("default", nil, "run_1") {
		t.Fatal("nil labels were accepted")
	}
}

func TestContainerNameIncludesNamespace(t *testing.T) {
	if got := containerName("sandboxtest", "run_1"); got != "agt-sandboxtest-run_1" {
		t.Fatalf("name %s", got)
	}
	if got := containerName("", "run_1"); got != "agt-default-run_1" {
		t.Fatalf("default name %s", got)
	}
}

func TestMissingContainerMatchesDockerLowercase(t *testing.T) {
	if !missingContainer("error: no such object: agt-t1-run_1") {
		t.Fatal("lowercase docker error was treated as a real inspect failure")
	}
	if !missingContainer("Error: No such container: agt-default-run_1") {
		t.Fatal("missing container was treated as a real inspect failure")
	}
	if missingContainer("permission denied") {
		t.Fatal("an unrelated inspect error was ignored")
	}
}

func TestNamespaceForDependsOnTheDataDir(t *testing.T) {
	one := NamespaceFor("/tmp/agt-one")
	two := NamespaceFor("/tmp/agt-two")
	if one == two || len(one) != 8 || one[:2] != "ns" {
		t.Fatalf("namespaces %s %s", one, two)
	}
	if NamespaceFor("/tmp/agt-one") != one {
		t.Fatal("namespace changed for the same data dir")
	}
}
