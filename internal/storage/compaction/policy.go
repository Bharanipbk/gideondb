// Package compaction selects bounded immutable segment inputs.
package compaction

import (
	"fmt"
	"sort"
)

type Segment struct {
	ID        string
	MaxLSN    uint64
	SizeBytes uint64
}

type Policy struct {
	FanIn         int
	SizeRatio     uint64
	MaxInputBytes uint64
	MaxSegments   int
}

type Plan struct {
	Inputs     []Segment
	InputBytes uint64
	Forced     bool
}

func DefaultPolicy() Policy {
	return Policy{FanIn: 4, SizeRatio: 4, MaxInputBytes: 64 << 20, MaxSegments: 8}
}

// Select chooses the oldest same-sized run. It never returns a plan exceeding
// MaxInputBytes; callers apply backpressure when the segment cap is reached and
// no bounded plan is possible.
func (p Policy) Select(segments []Segment) (Plan, error) {
	if p.FanIn < 2 || p.SizeRatio < 1 || p.MaxInputBytes == 0 || p.MaxSegments < p.FanIn {
		return Plan{}, fmt.Errorf("invalid compaction policy")
	}
	ordered := append([]Segment(nil), segments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].MaxLSN < ordered[j].MaxLSN })
	for start := 0; start < len(ordered); start++ {
		minimum := max(uint64(1), ordered[start].SizeBytes)
		var bytes uint64
		for end := start; end < len(ordered) && end-start < p.FanIn; end++ {
			if exceedsRatio(ordered[end].SizeBytes, minimum, p.SizeRatio) || ordered[end].SizeBytes > p.MaxInputBytes-bytes {
				break
			}
			bytes += ordered[end].SizeBytes
			if end-start+1 == p.FanIn {
				return Plan{Inputs: append([]Segment(nil), ordered[start:end+1]...), InputBytes: bytes}, nil
			}
		}
	}
	if len(ordered) < p.MaxSegments {
		return Plan{}, nil
	}
	var bytes uint64
	inputs := make([]Segment, 0, p.FanIn)
	for _, segment := range ordered {
		if len(inputs) == p.FanIn || segment.SizeBytes > p.MaxInputBytes-bytes {
			break
		}
		inputs, bytes = append(inputs, segment), bytes+segment.SizeBytes
	}
	if len(inputs) < 2 {
		return Plan{}, fmt.Errorf("segment limit reached without a bounded compaction plan")
	}
	return Plan{Inputs: inputs, InputBytes: bytes, Forced: true}, nil
}

func exceedsRatio(value, minimum, ratio uint64) bool {
	return value/minimum > ratio || (value/minimum == ratio && value%minimum != 0)
}
