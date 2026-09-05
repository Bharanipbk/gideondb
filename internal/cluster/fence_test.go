package cluster

import "testing"

func TestValidateFence(t *testing.T) {
	if err := ValidateFence(clusterTestID, localTestNode, 7, clusterTestID, localTestNode, 7); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		cluster, node string
		epoch         uint64
		kind          FenceErrorKind
	}{
		{"44444444444444444444444444444444", localTestNode, 7, FenceClusterMismatch},
		{clusterTestID, remoteTestNode, 7, FenceWrongNode},
		{clusterTestID, localTestNode, 6, FenceStaleEpoch},
		{clusterTestID, localTestNode, 8, FenceFutureEpoch},
	}
	for _, test := range tests {
		err := ValidateFence(clusterTestID, localTestNode, 7, test.cluster, test.node, test.epoch)
		fence, ok := err.(*FenceError)
		if !ok || fence.Kind != test.kind {
			t.Fatalf("error=%#v, want %s", err, test.kind)
		}
	}
}
