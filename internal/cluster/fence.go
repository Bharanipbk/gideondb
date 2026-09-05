package cluster

import "fmt"

type FenceErrorKind string

const (
	FenceClusterMismatch FenceErrorKind = "cluster_mismatch"
	FenceWrongNode       FenceErrorKind = "wrong_node"
	FenceStaleEpoch      FenceErrorKind = "stale_epoch"
	FenceFutureEpoch     FenceErrorKind = "future_epoch"
)

type FenceError struct {
	Kind     FenceErrorKind
	Expected string
	Received string
}

func (e *FenceError) Error() string {
	return fmt.Sprintf("%s: expected %s, received %s", e.Kind, e.Expected, e.Received)
}

// ValidateFence prevents an internal request intended for a different cluster,
// node, or metadata view from reaching a shard.
func ValidateFence(clusterID, nodeID string, epoch uint64, requestClusterID, targetNodeID string, requestEpoch uint64) error {
	if requestClusterID != clusterID {
		return &FenceError{Kind: FenceClusterMismatch, Expected: clusterID, Received: requestClusterID}
	}
	if targetNodeID != nodeID {
		return &FenceError{Kind: FenceWrongNode, Expected: nodeID, Received: targetNodeID}
	}
	if requestEpoch < epoch {
		return &FenceError{Kind: FenceStaleEpoch, Expected: fmt.Sprint(epoch), Received: fmt.Sprint(requestEpoch)}
	}
	if requestEpoch > epoch {
		return &FenceError{Kind: FenceFutureEpoch, Expected: fmt.Sprint(epoch), Received: fmt.Sprint(requestEpoch)}
	}
	return nil
}
