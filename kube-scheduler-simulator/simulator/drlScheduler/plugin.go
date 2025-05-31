package drlScheduler // Or your package name

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	framework "k8s.io/kubernetes/pkg/scheduler/framework"

	// Your base and energy packages
	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/base"
	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/energy"
)

const (
	drlScorerPluginName = "DRL"
	pythonServerURL     = "http://172.17.0.1:5001/predict_node_scores" // URL of your Flask server running on host
	// cpuModelUnknown        = "Unknown" // Defined in logger
	// nodeTypeLabelKey       = "type"    // Defined in logger
	// nodeTypeCloud          = "cloud"   // Defined in logger
	// nodeTypeEdge           = "edge"    // Defined in logger
)

const (
	Name = drlScorerPluginName // Name of the plugin, used in scheduler config
)

// DRLScorerPlugin uses a DRL model to score nodes.
type DRLScorerPlugin struct {
	handle     framework.Handle
	httpClient *http.Client // Re-use HTTP client
}

var _ framework.PreScorePlugin = &DRLScorerPlugin{}
var _ framework.ScorePlugin = &DRLScorerPlugin{}

// Name is the name of the plugin.
func (dsp *DRLScorerPlugin) Name() string {
	return drlScorerPluginName
}

// New initializes a new plugin and returns it.
func New(context context.Context, _ runtime.Object, h framework.Handle) (framework.Plugin, error) {
	return &DRLScorerPlugin{
		handle: h,
		httpClient: &http.Client{
			Timeout: 5 * time.Second, // Example timeout
		},
	}, nil
}

// --- Helper functions (similar to your logger, but adapted for DRLScorer state prep) ---

func (dsp *DRLScorerPlugin) getPodInfoForDRL(pod *v1.Pod) map[string]interface{} {
	cpuMillis, memMib := int64(0), int64(0)
	for _, container := range pod.Spec.Containers {
		if container.Resources.Requests != nil {
			cpuMillis += container.Resources.Requests.Cpu().MilliValue()
			memMib += container.Resources.Requests.Memory().Value() / (1024 * 1024)
		}
	}
	return map[string]interface{}{
		"pod_cpu_request":     cpuMillis / 1000,
		"pod_ram_request_mib": memMib,
	}
}

func (dsp *DRLScorerPlugin) getNodeFeaturesForDRL(nodeInfo *framework.NodeInfo, podToPlace *v1.Pod) (map[string]interface{}, error) {
	node := nodeInfo.Node()
	if node == nil {
		return nil, fmt.Errorf("node is nil in NodeInfo for %s", nodeInfo.Node().Name) // Should not happen if nodeInfo is valid
	}

	// Use the same resource info calculation as your logger for consistency
	// This gets current node state, and hypothetical state if podToPlace is added
	resourceCurrent, resourceHypotheticalAfterPod := dsp.getNodeResourceInfo(nodeInfo, podToPlace)

	// LGBM prediction for this node IF the podToPlace were placed on it.
	// This is the 'energy_before' feature for the DRL state.
	// It should use resourceHypotheticalAfterPod's load to predict the energy
	// if the pod *were* placed there.
	// Let's rename for clarity: energyIfPodPlaced
	energyIfPodPlaced, err := energy.PredictEnergyConsumption(&resourceHypotheticalAfterPod, node.Name)
	if err != nil {
		klog.Warningf("DRLScorer: Error predicting energy for node %s with pod %s: %v. Using high energy.", node.Name, podToPlace.Name, err)
		energyIfPodPlaced = 10000.0 // Assign a high (bad) energy if prediction fails
	}

	nodeTypeStr := "unknown"
	if strings.Contains(strings.ToLower(node.Name), "cloud") {
		nodeTypeStr = "cloud"
	} else if strings.Contains(strings.ToLower(node.Name), "edge") {
		nodeTypeStr = "edge"
	}

	cpuModelEnergyVal := dsp.getCPUEnergyValue(node) // Same as logger's helper

	return map[string]interface{}{
		"candidate_node_name":                 node.Name,
		"node_cpu_total":                      resourceCurrent.CPUTotal / 1000, // Total capacity
		"node_ram_total_mib":                  resourceCurrent.MemoryTotal / (1024 * 1024),
		"node_cpu_model_energy_val":           cpuModelEnergyVal,
		"node_type":                           nodeTypeStr,
		"energy_before_pod_placement_on_node": energyIfPodPlaced, // Crucial: energy *if* this pod lands here
		// Add other features if your DRL model expects them (e.g., current load before pod)
		// "node_cpu_allocatable_milli_before_pod": nodeInfo.Allocatable.MilliCPU,
		// "node_ram_allocatable_mib_before_pod": nodeInfo.Allocatable.Memory / (1024*1024),
	}, nil
}

// Helper: getCPUEnergyValue
func (dsp *DRLScorerPlugin) getCPUEnergyValue(node *v1.Node) float64 {
	cpuModel := base.CleanCPUModelName(node.Labels["cpu-model"]) // Ensure label key is correct
	cpuAvg := energy.GetCPUModelEncoding(cpuModel)               // Use the same encoding logic as in your logger

	return cpuAvg
}

// Helper: getNodeResourceInfo (same as in your logger or adapted)
func (dsp *DRLScorerPlugin) getNodeResourceInfo(nodeInfo *framework.NodeInfo, podForHypotheticalPlacement *v1.Pod) (current base.NodeResourceInfo, hypotheticalAfterPlacement base.NodeResourceInfo) {
	node := nodeInfo.Node()
	if node == nil {
		return
	} // Should be checked by caller

	cpuCapacity := node.Status.Capacity.Cpu().MilliValue()
	memCapacityBytes := node.Status.Capacity.Memory().Value()
	currentCpuUsed := nodeInfo.Requested.MilliCPU
	currentMemUsedBytes := nodeInfo.Requested.Memory

	current.NodeName = node.Name
	current.CPUModel = base.CleanCPUModelName(node.Labels["cpu-model"])
	current.CPUFreq = rand.Int63n(3200-2600) + 2600
	current.CPUTotal = cpuCapacity
	if cpuCapacity > 0 {
		current.CPUUsedPct = (currentCpuUsed * 100) / cpuCapacity
	}
	current.MemoryTotal = memCapacityBytes
	if memCapacityBytes > 0 {
		current.MemUsedPct = (currentMemUsedBytes * 100) / memCapacityBytes
	}

	hypotheticalAfterPlacement = current
	podCPUReq, podMemReqBytes := int64(0), int64(0)
	if podForHypotheticalPlacement != nil {
		tempPodCpu, tempPodMemMib := dsp.getPodResourceRequestsForDRL(podForHypotheticalPlacement) // Use specific helper if needed
		podCPUReq = tempPodCpu
		podMemReqBytes = tempPodMemMib * 1024 * 1024
	}
	hypotheticalCpuUsed := currentCpuUsed + podCPUReq
	hypotheticalMemUsedBytes := currentMemUsedBytes + podMemReqBytes
	if cpuCapacity > 0 {
		hypotheticalAfterPlacement.CPUUsedPct = (hypotheticalCpuUsed * 100) / cpuCapacity
	}
	if memCapacityBytes > 0 {
		hypotheticalAfterPlacement.MemUsedPct = (hypotheticalMemUsedBytes * 100) / memCapacityBytes
	}
	return
}

// Helper: getPodResourceRequestsForDRL (same as your logger's getPodResourceRequests)
func (dsp *DRLScorerPlugin) getPodResourceRequestsForDRL(pod *v1.Pod) (cpuMillis int64, memMib int64) {
	for _, container := range pod.Spec.Containers {
		if container.Resources.Requests != nil {
			cpuMillis += container.Resources.Requests.Cpu().MilliValue()
			memMib += container.Resources.Requests.Memory().Value() / (1024 * 1024)
		}
	}
	return
}

// DRLPreScoreStateKey is a key for CycleState.
const DRLPreScoreStateKey framework.StateKey = "DRLPreScoreState"

// DRLPreScoreData holds the scores received from the Python server.
type DRLPreScoreData struct {
	NodeScores map[string]float64 // nodeName -> score
}

// Clone an DRLPreScoreData.
func (d *DRLPreScoreData) Clone() framework.StateData {
	return &DRLPreScoreData{
		NodeScores: d.NodeScores, // Shallow copy is fine if map is not modified after set
	}
}

// PreScore phase: Collect all candidate node states and send to Python DRL server.
func (dsp *DRLScorerPlugin) PreScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodes []*framework.NodeInfo) *framework.Status {
	if len(nodes) == 0 {
		return framework.NewStatus(framework.Success, "No candidate nodes for DRL PreScore.")
	}

	podInfoForDRL := dsp.getPodInfoForDRL(pod)
	candidateNodesDataForDRL := make([]map[string]interface{}, 0, len(nodes))

	for _, node := range nodes {
		nodeInfo, err := dsp.handle.SnapshotSharedLister().NodeInfos().Get(node.Node().Name)
		if err != nil {
			klog.Warningf("DRLScorer: Error getting NodeInfo for %s in PreScore: %v. Skipping node.", node.Node().Name, err)
			continue
		}
		if nodeInfo.Node() == nil { // Should not happen if node came from framework
			klog.Warningf("DRLScorer: NodeInfo for %s has nil Node object in PreScore. Skipping node.", node.Node().Name)
			continue
		}

		// Get features for this node AS IF the current pod was placed on it
		nodeFeatures, err := dsp.getNodeFeaturesForDRL(nodeInfo, pod)
		if err != nil {
			klog.Warningf("DRLScorer: Error getting features for node %s in PreScore: %v. Skipping node.", node.Node().Name, err)
			continue
		}
		candidateNodesDataForDRL = append(candidateNodesDataForDRL, nodeFeatures)
	}

	if len(candidateNodesDataForDRL) == 0 {
		klog.Warningf("DRLScorer: No valid candidate node data to send for pod %s/%s.", pod.Namespace, pod.Name)
		// Store empty scores so Score phase knows nothing was processed
		state.Write(DRLPreScoreStateKey, &DRLPreScoreData{NodeScores: make(map[string]float64)})
		return framework.NewStatus(framework.Success)
	}

	requestPayload := map[string]interface{}{
		"pod_info":        podInfoForDRL,
		"candidate_nodes": candidateNodesDataForDRL,
	}

	payloadBytes, err := json.Marshal(requestPayload)
	if err != nil {
		klog.Errorf("DRLScorer: Error marshalling request payload for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return framework.NewStatus(framework.Error, "Failed to prepare DRL request.")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", pythonServerURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		klog.Errorf("DRLScorer: Error creating request for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return framework.NewStatus(framework.Error, "Failed to create DRL request.")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := dsp.httpClient.Do(req)
	if err != nil {
		klog.Errorf("DRLScorer: Error sending request to Python server for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return framework.NewStatus(framework.Error, "DRL server request failed.")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		klog.Errorf("DRLScorer: Python server returned error %d for pod %s/%s: %s", resp.StatusCode, pod.Namespace, pod.Name, string(bodyBytes))
		return framework.NewStatus(framework.Error, fmt.Sprintf("DRL server error: %d", resp.StatusCode))
	}

	var responseData map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&responseData); err != nil {
		klog.Errorf("DRLScorer: Error decoding Python server response for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return framework.NewStatus(framework.Error, "Failed to decode DRL response.")
	}

	nodeScores, ok := responseData["node_scores"]
	if !ok {
		klog.Errorf("DRLScorer: 'node_scores' not found in Python server response for pod %s/%s.", pod.Namespace, pod.Name)
		return framework.NewStatus(framework.Error, "Invalid DRL response format.")
	}

	klog.V(4).Infof("DRLScorer: Received scores for pod %s/%s: %v", pod.Namespace, pod.Name, nodeScores)
	state.Write(DRLPreScoreStateKey, &DRLPreScoreData{NodeScores: nodeScores})
	return framework.NewStatus(framework.Success)
}

// Score retrieves the DRL score from CycleState (populated by PreScore).
func (dsp *DRLScorerPlugin) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	data, err := state.Read(DRLPreScoreStateKey)
	if err != nil {
		klog.Errorf("DRLScorer: Error reading DRLPreScoreStateKey from cycle state for pod %s/%s, node %s: %v", pod.Namespace, pod.Name, nodeName, err)
		return framework.MinNodeScore, framework.AsStatus(fmt.Errorf("reading DRLPreScoreStateKey: %w", err))
	}

	preScoreData, ok := data.(*DRLPreScoreData)
	if !ok {
		klog.Errorf("DRLScorer: Invalid data type for DRLPreScoreStateKey for pod %s/%s, node %s", pod.Namespace, pod.Name, nodeName)
		return framework.MinNodeScore, framework.AsStatus(fmt.Errorf("invalid data type for DRLPreScoreStateKey"))
	}

	// DRL Q-values can be positive or negative. Framework scores are int64.
	// We need to scale/convert. Higher Q-value should mean better score.
	// A simple scaling: multiply by 100 and cast. Adjust as needed.
	// Ensure it fits within framework.MinNodeScore and framework.MaxNodeScore if necessary.
	// For now, a direct cast after scaling.
	allQValues := make([]float64, 0, len(preScoreData.NodeScores))
	for _, q := range preScoreData.NodeScores {
		allQValues = append(allQValues, q)
	}

	if len(allQValues) == 0 {
		// No scores were returned from DRL for any node for this pod
		// Or this specific nodeName was not in the map (should be handled)
		klog.V(3).Infof("DRLScorer: No Q-values available for pod %s/%s. Node %s gets MinNodeScore.", pod.Namespace, pod.Name, nodeName)
		return framework.MinNodeScore, framework.NewStatus(framework.Success)
	}

	minQ := allQValues[0]
	maxQ := allQValues[0]
	for _, q := range allQValues {
		if q < minQ {
			minQ = q
		}
		if q > maxQ {
			maxQ = q
		}
	}

	currentQ, found := preScoreData.NodeScores[nodeName]
	if !found {
		klog.V(3).Infof("DRLScorer: Score for node %s not found in PreScore data for pod %s/%s. Assigning MinNodeScore.", nodeName, pod.Namespace, pod.Name)
		return framework.MinNodeScore, framework.NewStatus(framework.Success)
	}

	var scaledScore float64
	if maxQ == minQ { // All nodes have the same Q-value
		scaledScore = 50.0 // Assign a neutral middle score
	} else {
		normalizedQ := (currentQ - minQ) / (maxQ - minQ)
		scaledScore = normalizedQ * 100.0
	}

	intScore := int64(math.Round(scaledScore))

	// Clamp to Kubernetes framework's score range
	if intScore < framework.MinNodeScore {
		intScore = framework.MinNodeScore
	}
	if intScore > framework.MaxNodeScore { // framework.MaxNodeScore is typically 100
		intScore = framework.MaxNodeScore
	}

	klog.V(5).Infof("DRLScorer: Pod %s/%s on Node %s, DRL Q: %.2f (min:%.2f, max:%.2f), NormalizedQ: %.2f, K8s Score: %d",
		pod.Namespace, pod.Name, nodeName, currentQ, minQ, maxQ, scaledScore/100.0, intScore)
	return intScore, framework.NewStatus(framework.Success)

}

// ScoreExtensions returns nil as this plugin does not currently normalize scores.
func (dsp *DRLScorerPlugin) ScoreExtensions() framework.ScoreExtensions {
	return nil
}
