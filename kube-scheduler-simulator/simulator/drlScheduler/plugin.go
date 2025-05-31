package drlScheduler

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/base"
	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/energy"
)

const (
	// Name is the plugin name used in logs and configuration.
	Name = "DRL"
)

// Ensure we implement the Score extension.
var _ framework.ScorePlugin = &DRLScorer{}
var _ framework.PreScorePlugin = &DRLScorer{}

// NodeResourceInfo holds resource information for a node

// ClusterState holds resource information for all nodes
type ClusterState struct {
	Nodes     []base.NodeResourceInfo `json:"nodes"`
	Timestamp int64                   `json:"timestamp"`
}

// energyScoreState holds the energy scores for nodes
type energyScoreState struct {
	scores map[string]float64
}

// New initializes a new plugin and returns it.
// New initializes the plugin.
// New initializes the plugin.
func New(_ context.Context, arg runtime.Object, h framework.Handle) (framework.Plugin, error) {
	// You could parse arguments here if needed, similar to the NodeNumber example
	// var args SomeArgsType
	// if arg != nil {
	//     err := frameworkruntime.DecodeInto(arg, &args)
	//     if err != nil {
	//         return nil, fmt.Errorf("failed to decode args: %w", err)
	//     }
	// }

	return &DRLScorer{handle: h}, nil
}

// Clone implements the StateData interface
func (e *energyScoreState) Clone() framework.StateData {
	if e == nil {
		return nil
	}
	newScores := make(map[string]float64, len(e.scores))
	for k, v := range e.scores {
		newScores[k] = v
	}
	return &energyScoreState{scores: newScores}
}

// DRLScorer holds the scheduler handle for accessing the snapshot.
type DRLScorer struct {
	handle framework.Handle
}

// Name returns the plugin's name.
func (pl *DRLScorer) Name() string {
	return Name
}

// calculateNodeResources calculates total and remaining resources for each node
func (pl *DRLScorer) calculateNodeResources(ctx context.Context) (*ClusterState, error) {
	snapshot := pl.handle.SnapshotSharedLister()
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot is nil")
	}

	nodeInfos, err := snapshot.NodeInfos().List()
	if err != nil {
		return nil, fmt.Errorf("error listing nodes: %v", err)
	}

	clusterState := &ClusterState{
		Nodes:     make([]base.NodeResourceInfo, 0, len(nodeInfos)),
		Timestamp: time.Now().Unix(),
	}

	for _, nodeInfo := range nodeInfos {
		if nodeInfo == nil || nodeInfo.Node() == nil {
			continue
		}

		node := nodeInfo.Node()

		// Get capacity from node status
		cpuCapacity := node.Status.Capacity.Cpu().MilliValue()
		memCapacity := node.Status.Capacity.Memory().Value()

		// Calculate used resources by summing up all pod requests
		var cpuUsed, memUsed int64
		for _, podInfo := range nodeInfo.Pods {
			if podInfo == nil || podInfo.Pod == nil {
				continue
			}

			for _, container := range podInfo.Pod.Spec.Containers {
				cpuUsed += container.Resources.Requests.Cpu().MilliValue()
				memUsed += container.Resources.Requests.Memory().Value()
			}
		}

		// Calculate remaining resources
		cpuRemaining := cpuCapacity - cpuUsed
		memRemaining := memCapacity - memUsed

		// Calculate used percentage
		cpuUsedPct := (cpuUsed * 100) / cpuCapacity
		memUsedPct := (memUsed * 100) / memCapacity

		cpuFreq := rand.Int63n(3200-2600) + 2600 // Example frequency in MHz between 2600 and 3200

		nodeResourceInfo := base.NodeResourceInfo{
			NodeName:        node.Name,
			CPUModel:        node.Labels["cpu_model"],
			CPUFreq:         cpuFreq, // Example frequency in MHz
			CPUTotal:        cpuCapacity,
			CPURemaining:    cpuRemaining,
			CPUUsedPct:      cpuUsedPct,
			MemoryTotal:     memCapacity,
			MemoryRemaining: memRemaining,
			MemUsedPct:      memUsedPct,
		}
		klog.Infof("Node %s: CPU Total: %d, CPU Remaining: %d, Memory Total: %d, Memory Remaining: %d",
			node.Name, cpuCapacity, cpuRemaining, memCapacity, memRemaining)
		clusterState.Nodes = append(clusterState.Nodes, nodeResourceInfo)
	}

	return clusterState, nil
}

// PreScore is called before Score to calculate and send resource information once per scheduling cycle
func (pl *DRLScorer) PreScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodes []*framework.NodeInfo) *framework.Status {
	// Calculate energy score for each node based on the functions in energy.go and the information clusterState (might need to node per node)
	nodeState, err := pl.calculateNodeResources(ctx)
	energyScores := make(map[string]float64)
	if err != nil {
		klog.ErrorS(err, "Failed to calculate node resources")
		return framework.NewStatus(framework.Error)
	}

	for _, nodeInfo := range nodeState.Nodes {
		energyScore, err := energy.PredictEnergyConsumption(&nodeInfo, nodeInfo.NodeName)
		if err != nil {
			klog.ErrorS(err, "Failed to calculate energy score via LightGBM model", "node", nodeInfo.NodeName)
			return framework.NewStatus(framework.Error)
		}
		klog.V(4).InfoS("Energy score calculated", "node", nodeInfo.NodeName, "score", energyScore)
		energyScores[nodeInfo.NodeName] = energyScore
	}

	// Save the energy score for each node in the cycle state
	state.Write("energyScore", &energyScoreState{scores: energyScores})

	return nil
}

// EventsToRegister returns the events to register
func (pl *DRLScorer) EventsToRegister() []framework.ClusterEvent {
	return []framework.ClusterEvent{
		{Resource: framework.Node, ActionType: framework.Add},
		{Resource: framework.Node, ActionType: framework.Update},
		{Resource: framework.Pod, ActionType: framework.Add},
		{Resource: framework.Pod, ActionType: framework.Update},
		{Resource: framework.Pod, ActionType: framework.Delete},
	}
}

// Score generates a random score for the node
func (pl *DRLScorer) Score(
	ctx context.Context,
	state *framework.CycleState,
	pod *v1.Pod,
	nodeName string,
) (int64, *framework.Status) {
	klog.InfoS("Execute Score on DRL plugin", "pod", klog.KObj(pod), "node", nodeName)

	// Get the energy scores from the cycle state
	energyScores, err := state.Read("energyScore")
	if err != nil {
		return 0, framework.NewStatus(framework.Error)
	}
	// return the energy score for the node
	energyScore := energyScores.(*energyScoreState)
	if energyScore == nil {
		return 0, framework.NewStatus(framework.Error)
	}

	// Get the energy score for the node
	score := energyScore.scores[nodeName]
	return int64(score), framework.NewStatus(framework.Success)
}

// ScoreExtensions returns nil as we don't implement NormalizeScore.
func (pl *DRLScorer) ScoreExtensions() framework.ScoreExtensions {
	return nil
}
