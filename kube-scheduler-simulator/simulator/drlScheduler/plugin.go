package drlScheduler

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/proto"
)

const (
	// Name is the plugin name used in logs and configuration.
	Name = "DRL"
)

// Ensure we implement the Score extension.
var _ framework.ScorePlugin = &ResourceAwareScorer{}
var _ framework.PreScorePlugin = &ResourceAwareScorer{}

// NodeResourceInfo holds resource information for a node
type NodeResourceInfo struct {
	NodeName        string `json:"nodeName"`
	CPUTotal        uint32 `json:"cpuTotal"`        // in millicores
	CPURemaining    uint32 `json:"cpuRemaining"`    // in millicores
	CPUUsedPct      int64  `json:"cpuUsedPct"`      // in percentage
	CPUModel        string `json:"cpuModel"`        // CPU model name
	CPUFreq         int64  `json:"cpuFreq"`         // CPU frequency in MHz
	MemoryTotal     int64  `json:"memoryTotal"`     // in bytes
	MemoryRemaining int64  `json:"memoryRemaining"` // in bytes
	MemUsedPct      int64  `json:"memUsedPct"`      // in percentage
}

// ClusterState holds resource information for all nodes
type ClusterState struct {
	Nodes     []NodeResourceInfo `json:"nodes"`
	Timestamp int64              `json:"timestamp"`
}

// energyScoreState holds the energy scores for nodes
type energyScoreState struct {
	scores map[string]float64
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

// ResourceAwareScorer holds the scheduler handle for accessing the snapshot.
type ResourceAwareScorer struct {
	handle     framework.Handle
	grpcClient proto.EnergyServiceClient
	grpcConn   *grpc.ClientConn
}

// Name returns the plugin's name.
func (pl *ResourceAwareScorer) Name() string {
	return Name
}

// New initializes the plugin.
func New(_ context.Context, arg runtime.Object, h framework.Handle) (framework.Plugin, error) {
	// Establish gRPC connection to energy server
	conn, err := grpc.NewClient("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		klog.ErrorS(err, "Failed to connect to energy service")
		return nil, fmt.Errorf("failed to connect to energy service: %w", err)
	}

	client := proto.NewEnergyServiceClient(conn)

	return &ResourceAwareScorer{
		handle:     h,
		grpcClient: client,
		grpcConn:   conn,
	}, nil
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

		// Calculate used percentage
		cpuUsedPct := (cpuUsed * 100) / cpuCapacity
		memUsedPct := (memUsed * 100) / memCapacity

		cpuFreq := rand.Int63n(3200-2600) + 2600 // Example frequency in MHz between 2600 and 3200

		nodeResourceInfo := NodeResourceInfo{
			NodeName:        node.Name,
			CPUModel:        node.Labels["cpu_model"],
			CPUFreq:         cpuFreq, // Example frequency in MHz
			CPUTotal:        uint32(cpuCapacity),
			CPURemaining:    uint32(cpuRemaining),
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
func (pl *ResourceAwareScorer) PreScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodes []*framework.NodeInfo) *framework.Status {
	// Calculate energy score for each node based on the functions in energy.go and the information clusterState (might need to node per node)
	clusterState, err := pl.calculateNodeResources(ctx)
	energyScores := make(map[string]float64)
	if err != nil {
		klog.ErrorS(err, "Failed to calculate node resources")
		return framework.NewStatus(framework.Error)
	}

	for _, nodeInfo := range clusterState.Nodes {
		energyScore, err := pl.getEnergyScoreViaGRPC(ctx, nodeInfo)
		if err != nil {
			klog.ErrorS(err, "Failed to calculate energy score via gRPC", "node", nodeInfo.NodeName)
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
func (pl *ResourceAwareScorer) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

// getEnergyScoreViaGRPC requests energy prediction from the gRPC server
func (pl *ResourceAwareScorer) getEnergyScoreViaGRPC(ctx context.Context, nodeInfo NodeResourceInfo) (float64, error) {
	// Create prediction request
	request := &proto.PredictionRequest{
		CpuModel:        nodeInfo.CPUModel,
		RamCapacityGb:   float64(nodeInfo.MemoryTotal) / (1024 * 1024 * 1024), // Convert bytes to GB
		CpuFreqMhz:      int32(nodeInfo.CPUFreq),
		NumCores:        int32(nodeInfo.CPUTotal / 1000),   // Convert millicores to cores
		AchievedLoadPct: float64(nodeInfo.CPUUsedPct + 10), // Add 10% to achieved load
	}

	// Call gRPC service with timeout
	grpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	response, err := pl.grpcClient.PredictPower(grpcCtx, request)
	if err != nil {
		return 0, fmt.Errorf("gRPC call failed: %w", err)
	}

	if response.Status != "success" {
		return 0, fmt.Errorf("prediction failed: %s", response.Error)
	}

	return response.PredictedPowerWatts, nil
}
