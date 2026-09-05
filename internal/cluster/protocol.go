package cluster

import "fmt"

const (
	MinClusterProtocolVersion uint32 = 1
	ClusterProtocolVersion    uint32 = 2
)

// NegotiateProtocol selects the newest mutually supported cluster protocol.
func NegotiateProtocol(localMin, localMax, remoteMin, remoteMax uint32) (uint32, error) {
	if localMin == 0 || remoteMin == 0 || localMin > localMax || remoteMin > remoteMax {
		return 0, fmt.Errorf("invalid cluster protocol range")
	}
	minimum := max(localMin, remoteMin)
	maximum := min(localMax, remoteMax)
	if minimum > maximum {
		return 0, fmt.Errorf("cluster protocol ranges do not overlap")
	}
	return maximum, nil
}
