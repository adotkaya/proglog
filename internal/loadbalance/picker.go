// START: picker
package loadbalance

import (
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
)

// END: picker

// START: init
func init() {
	balancer.Register(
		base.NewBalancerBuilder(
			Name,
			&Picker{},
			base.Config{HealthCheck: true},
		),
	)
}

// END: init

// START: picker_type
var _ balancer.Picker = (*Picker)(nil)

type Picker struct {
	mu        sync.RWMutex
	leader    balancer.SubConn
	followers []balancer.SubConn
	current   uint64
}

// END: picker_type

// START: picker_build
func (p *Picker) Build(buildInfo base.PickerBuildInfo) balancer.Picker {
	p.mu.Lock()
	defer p.mu.Unlock()
	var followers []balancer.SubConn
	for sc, scInfo := range buildInfo.ReadySCs {
		isLeader := scInfo.Address.Attributes.Value("is_leader").(bool)
		if isLeader {
			p.leader = sc
			continue
		}
		followers = append(followers, sc)
	}
	p.followers = followers
	return p
}

// END: picker_build

// START: picker_pick
func (p *Picker) Pick(info balancer.PickInfo) (
	balancer.PickResult, error,
) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var result balancer.PickResult
	if info.FullMethodName == "/log.v1.Log/Produce" {
		result.SubConn = p.leader
	} else if len(p.followers) > 0 {
		offset := atomic.AddUint64(&p.current, uint64(1))
		result.SubConn = p.followers[offset%uint64(len(p.followers))]
	}
	if result.SubConn == nil {
		return result, balancer.ErrNoSubConnAvailable
	}
	return result, nil
}

// END: picker_pick