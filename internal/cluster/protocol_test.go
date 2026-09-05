package cluster

import "testing"

func TestNegotiateProtocolSupportsRollingUpgradeOverlap(t *testing.T) {
	version, err := NegotiateProtocol(1, 2, 1, 1)
	if err != nil || version != 1 {
		t.Fatalf("legacy overlap version=%d err=%v", version, err)
	}
	version, err = NegotiateProtocol(1, 2, 2, 3)
	if err != nil || version != 2 {
		t.Fatalf("new overlap version=%d err=%v", version, err)
	}
	if _, err := NegotiateProtocol(1, 2, 3, 4); err == nil {
		t.Fatal("accepted non-overlapping protocol ranges")
	}
	if _, err := NegotiateProtocol(0, 2, 1, 2); err == nil {
		t.Fatal("accepted invalid protocol range")
	}
}
