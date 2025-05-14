package drlScheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const (
	// Name is the plugin name used in logs and configuration.
	Name = "DRL"
	// stateKey is the key used to store whether we've sent resource info in this cycle
	stateKey = "DRLSchedulerResourceInfoSent"
)

// Ensure we implement the Score extension.
var _ framework.ScorePlugin = &ResourceAwareScorer{}
var _ framework.PreScorePlugin = &ResourceAwareScorer{}

// NodeResourceInfo holds resource information for a node
type NodeResourceInfo struct {
	NodeName        string `json:"nodeName"`
	CPUTotal        int64  `json:"cpuTotal"`        // in millicores
	CPURemaining    int64  `json:"cpuRemaining"`    // in millicores
	MemoryTotal     int64  `json:"memoryTotal"`     // in bytes
	MemoryRemaining int64  `json:"memoryRemaining"` // in bytes
}

// ClusterState holds resource information for all nodes
type ClusterState struct {
	Nodes     []NodeResourceInfo `json:"nodes"`
	Timestamp int64              `json:"timestamp"`
}

// ResourceAwareScorer holds the scheduler handle for accessing the snapshot.
type ResourceAwareScorer struct {
	handle framework.Handle
}

// resourceSentState is a cycle state for tracking if resource info has been sent
type resourceSentState struct {
	sent bool
}

// Clone implements the StateData interface
func (s *resourceSentState) Clone() framework.StateData {
	return &resourceSentState{sent: s.sent}
}

// Name returns the plugin's name.
func (pl *ResourceAwareScorer) Name() string {
	return Name
}

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

	return &ResourceAwareScorer{handle: h}, nil
}

// calculateNodeResources calculates total and remaining resources for each node
func (pl *ResourceAwareScorer) calculateNodeResources(ctx context.Context) (*ClusterState, error) {
	snapshot := pl.handle.SnapshotSharedLister()
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot is nil")
	}

	nodeInfos, err := snapshot.NodeInfos().List()
	if err != nil {
		return nil, fmt.Errorf("error listing nodes: %v", err)
	}

	clusterState := &ClusterState{
		Nodes:     make([]NodeResourceInfo, 0, len(nodeInfos)),
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

		nodeResourceInfo := NodeResourceInfo{
			NodeName:        node.Name,
			CPUTotal:        cpuCapacity,
			CPURemaining:    cpuRemaining,
			MemoryTotal:     memCapacity,
			MemoryRemaining: memRemaining,
		}

		clusterState.Nodes = append(clusterState.Nodes, nodeResourceInfo)
	}

	return clusterState, nil
}

// sendResourceInfoToEndpoint sends the resource information to localhost:5000
func (pl *ResourceAwareScorer) sendResourceInfoToEndpoint(clusterState *ClusterState) error {
	jsonData, err := json.Marshal(clusterState)
	if err != nil {
		return fmt.Errorf("error marshaling resource data: %v", err)
	}

	klog.V(4).Infof("Sending cluster state to endpoint: %s", string(jsonData))

	resp, err := http.Post("http://172.17.0.1:5000/cluster-info", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error sending resource data to endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("endpoint returned non-OK status: %d", resp.StatusCode)
	}

	return nil
}

// PreScore is called before Score to calculate and send resource information once per scheduling cycle
func (pl *ResourceAwareScorer) PreScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodes []*framework.NodeInfo) *framework.Status {
	klog.InfoS("Execute PreScore on DRL plugin", "pod", klog.KObj(pod))

	// Calculate and send resource information
	clusterState, err := pl.calculateNodeResources(ctx)
	if err != nil {
		klog.ErrorS(err, "Failed to calculate node resources")
		// Continue with scoring even if resource calculation fails
	} else {
		err = pl.sendResourceInfoToEndpoint(clusterState)
		if err != nil {
			klog.ErrorS(err, "Failed to send resource info to endpoint")
			// Continue with scoring even if sending fails
		}
	}

	// Mark that we've sent the resource info in this cycle
	state.Write(stateKey, &resourceSentState{sent: true})

	return nil
}

// EventsToRegister returns the events to register
func (pl *ResourceAwareScorer) EventsToRegister() []framework.ClusterEvent {
	return []framework.ClusterEvent{
		{Resource: framework.Node, ActionType: framework.Add},
		{Resource: framework.Node, ActionType: framework.Update},
		{Resource: framework.Pod, ActionType: framework.Add},
		{Resource: framework.Pod, ActionType: framework.Update},
		{Resource: framework.Pod, ActionType: framework.Delete},
	}
}

// Score generates a random score for the node
func (pl *ResourceAwareScorer) Score(
	ctx context.Context,
	state *framework.CycleState,
	pod *v1.Pod,
	nodeName string,
) (int64, *framework.Status) {
	klog.InfoS("Execute Score on DRL plugin", "pod", klog.KObj(pod), "node", nodeName)

	// Generate a random score between 40 and 70
	rand.Seed(time.Now().UnixNano())
	score := rand.Int63n(31) + 40 // Random number between 40 and 70
	klog.Infof("Node %s scored %d for pod %s/%s", nodeName, score, pod.Namespace, pod.Name)

	return score, framework.NewStatus(framework.Success)
}

// ScoreExtensions returns nil as we don't implement NormalizeScore.
func (pl *ResourceAwareScorer) ScoreExtensions() framework.ScoreExtensions {
	return nil
}
