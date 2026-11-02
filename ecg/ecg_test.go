package ecg

import "testing"

func TestUnambiguousIdentities(t *testing.T) {
	if NodeKey("a:b", "c", "d", "e") == NodeKey("a", "b:c", "d", "e") {
		t.Fatal("ambiguous identity encoding")
	}
	if len(NodeKey("api", "1", "v1", "default")) != 32 || len(Chain("", "node")) != 32 {
		t.Fatal("expected 128-bit fingerprints")
	}
	if Chain(Chain("", "a"), "b") == Chain(Chain("", "b"), "a") {
		t.Fatal("path order lost")
	}
}
