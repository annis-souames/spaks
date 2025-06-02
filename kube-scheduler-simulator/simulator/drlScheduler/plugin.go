package drlScheduler // Or your package name

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	// "math" // No longer needed for complex scaling if direct choice
	"math/rand"
	"net/http"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	framework "k8s.io/kubernetes/pkg/scheduler/framework"

	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/base"
	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/energy"
)

const (
	drlScorerPluginName = "DRL"
	pythonServerURL     = "http://172.17.0.1:8080/predict" // UPDATED Endpoint name
)

// DRLScorerPlugin uses a DRL model to select a node.
type DRLScorerPlugin struct {
	handle     framework.Handle
	httpClient *http.Client
}

var _ framework.PreScorePlugin = &DRLScorerPlugin{}
var _ framework.ScorePlugin = &DRLScorerPlugin{}

const (
	Name string = drlScorerPluginName
)

func (dsp *DRLScorerPlugin) Name() string {
	return drlScorerPluginName
}

func New(context context.Context, _ runtime.Object, h framework.Handle) (framework.Plugin, error) {
	klog.V(3).Infof("DRLScorerPlugin: New plugin instance created.")
	return &DRLScorerPlugin{
		handle: h,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

// --- Helper functions (keep getPodInfoForRequest, getNodeFeaturesForRequest, getCPUEnergyValueFromNode, getNodeResourceInfo, getPodRequestsForCalc as before) ---
// getPodInfoForRequest prepares the "pod" part of the JSON request.
func (dsp *DRLScorerPlugin) getPodInfoForRequest(pod *v1.Pod) map[string]interface{} {
	cpuMillis, memMib := int64(0), int64(0)
	for _, container := range pod.Spec.Containers {
		if container.Resources.Requests != nil {
			cpuMillis += container.Resources.Requests.Cpu().MilliValue()
			memMib += container.Resources.Requests.Memory().Value() / (1024 * 1024)
		}
	}
	return map[string]interface{}{
		"cpu_request_milli": cpuMillis,
		"ram_request_mib":   memMib,
	}
}

// getNodeFeaturesForRequest prepares one "candidate" entry for the JSON request.
func (dsp *DRLScorerPlugin) getNodeFeaturesForRequest(nodeInfo *framework.NodeInfo, podToPlace *v1.Pod) (map[string]interface{}, error) {
	node := nodeInfo.Node()
	if node == nil {
		originalNodeName := "unknownNodeInNodeInfo"
		if nodeInfo != nil && nodeInfo.Node() != nil {
			originalNodeName = nodeInfo.Node().Name
		}
		return nil, fmt.Errorf("node object is nil within NodeInfo for (presumably) node %s", originalNodeName)
	}

	resourceCurrent, resourceHypotheticalAfterPod := dsp.getNodeResourceInfo(nodeInfo, podToPlace)
	energyIfPodPlacedOnThisNode, err := energy.PredictEnergyConsumption(&resourceHypotheticalAfterPod, node.Name)
	if err != nil {
		klog.Warningf("DRLScorer: Error predicting energy for node %s with pod %s: %v. Using a default high energy value.", node.Name, podToPlace.Name, err)
		energyIfPodPlacedOnThisNode = 100000.0
	}
	nodeTypeStr := "unknown"
	if strings.Contains(strings.ToLower(node.Name), "cloud") {
		nodeTypeStr = "cloud"
	} else if strings.Contains(strings.ToLower(node.Name), "edge") {
		nodeTypeStr = "edge"
	}
	cpuModelEnergyVal := dsp.getCPUEnergyValueFromNode(node)
	return map[string]interface{}{
		"name":                 node.Name,
		"cpu_total_milli":      resourceCurrent.CPUTotal,
		"ram_total_mib":        resourceCurrent.MemoryTotal / (1024 * 1024),
		"cpu_model_energy_val": cpuModelEnergyVal,
		"node_type":            nodeTypeStr,
		"energy_before":        energyIfPodPlacedOnThisNode,
	}, nil
}

func (dsp *DRLScorerPlugin) getCPUEnergyValueFromNode(node *v1.Node) float64 {
	cpuModelName := base.CleanCPUModelName(node.Labels["cpu-model"])
	return energy.GetCPUModelEncoding(cpuModelName)
}

func (dsp *DRLScorerPlugin) getNodeResourceInfo(nodeInfo *framework.NodeInfo, podForHypotheticalPlacement *v1.Pod) (current base.NodeResourceInfo, hypotheticalAfterPlacement base.NodeResourceInfo) {
	node := nodeInfo.Node()
	if node == nil {
		return
	}
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
	} else {
		current.CPUUsedPct = 100
	} // Assume fully used if capacity is 0
	current.MemoryTotal = memCapacityBytes
	if memCapacityBytes > 0 {
		current.MemUsedPct = (currentMemUsedBytes * 100) / memCapacityBytes
	} else {
		current.MemUsedPct = 100
	}
	hypotheticalAfterPlacement = current
	podCPUReq, podMemReqBytes := int64(0), int64(0)
	if podForHypotheticalPlacement != nil {
		tempPodCpu, tempPodMemMib := dsp.getPodRequestsForCalc(podForHypotheticalPlacement)
		podCPUReq = tempPodCpu
		podMemReqBytes = tempPodMemMib * 1024 * 1024
	}
	hypotheticalCpuUsed := currentCpuUsed + podCPUReq
	hypotheticalMemUsedBytes := currentMemUsedBytes + podMemReqBytes
	if cpuCapacity > 0 {
		hypotheticalAfterPlacement.CPUUsedPct = (hypotheticalCpuUsed * 100) / cpuCapacity
	} else {
		hypotheticalAfterPlacement.CPUUsedPct = 100
	}
	if memCapacityBytes > 0 {
		hypotheticalAfterPlacement.MemUsedPct = (hypotheticalMemUsedBytes * 100) / memCapacityBytes
	} else {
		hypotheticalAfterPlacement.MemUsedPct = 100
	}
	return
}
func (dsp *DRLScorerPlugin) getPodRequestsForCalc(pod *v1.Pod) (cpuMillis int64, memMib int64) {
	for _, container := range pod.Spec.Containers {
		if container.Resources.Requests != nil {
			cpuMillis += container.Resources.Requests.Cpu().MilliValue()
			memMib += container.Resources.Requests.Memory().Value() / (1024 * 1024)
		}
	}
	return
}

// --- End of Helper Functions ---

const DRLPreScoreStateKey framework.StateKey = "DRLRecommendedNodeState" // Renamed for clarity

// DRLRecommendedNodeData holds the recommended node from the Python server.
type DRLRecommendedNodeData struct {
	RecommendedNodeName string
	PodName             string // Store for debugging/logging in Score
	PodNamespace        string
}

// Clone a DRLRecommendedNodeData.
func (d *DRLRecommendedNodeData) Clone() framework.StateData {
	return &DRLRecommendedNodeData{ // Return a new struct with copied values
		RecommendedNodeName: d.RecommendedNodeName,
		PodName:             d.PodName,
		PodNamespace:        d.PodNamespace,
	}
}

// PreScore phase: Collect all candidate node states, send to Python DRL server,
// and store the single recommended node name.
func (dsp *DRLScorerPlugin) PreScore(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod, nodesToEvaluate []*framework.NodeInfo) *framework.Status {
	if len(nodesToEvaluate) == 0 {
		klog.V(4).Info("DRLScorer: No candidate nodes for DRL PreScore.")
		// Store empty recommendation if no nodes
		cycleState.Write(DRLPreScoreStateKey, &DRLRecommendedNodeData{RecommendedNodeName: ""})
		return framework.NewStatus(framework.Success)
	}

	podRequestData := dsp.getPodInfoForRequest(pod)
	candidateNodesFeaturesList := make([]map[string]interface{}, 0, len(nodesToEvaluate))

	for _, nodeInfo := range nodesToEvaluate {
		if nodeInfo.Node() == nil {
			klog.Warningf("DRLScorer: NodeInfo with nil Node encountered in PreScore. Skipping.")
			continue
		}
		nodeFeatures, err := dsp.getNodeFeaturesForRequest(nodeInfo, pod)
		if err != nil {
			klog.Warningf("DRLScorer: Error getting features for node %s in PreScore: %v. Skipping node.", nodeInfo.Node().Name, err)
			continue
		}
		candidateNodesFeaturesList = append(candidateNodesFeaturesList, nodeFeatures)
	}

	if len(candidateNodesFeaturesList) == 0 {
		klog.Warningf("DRLScorer: No valid candidate node data to send for pod %s/%s.", pod.Namespace, pod.Name)
		cycleState.Write(DRLPreScoreStateKey, &DRLRecommendedNodeData{RecommendedNodeName: ""})
		return framework.NewStatus(framework.Success)
	}

	requestPayload := map[string]interface{}{
		"pod":        podRequestData,
		"candidates": candidateNodesFeaturesList,
	}

	payloadBytes, err := json.Marshal(requestPayload)
	if err != nil {
		klog.Errorf("DRLScorer: Error marshalling request payload for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return framework.NewStatus(framework.Error, "Failed to prepare DRL request.")
	}

	klog.V(5).Infof("DRLScorer: Sending payload to Python server at %s: %s", pythonServerURL, string(payloadBytes))

	req, err := http.NewRequestWithContext(ctx, "POST", pythonServerURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		klog.Errorf("DRLScorer: Error creating request for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return framework.NewStatus(framework.Error, "Failed to create DRL request.")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := dsp.httpClient.Do(req)
	if err != nil {
		klog.Errorf("DRLScorer: Error sending request to Python server for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		cycleState.Write(DRLPreScoreStateKey, &DRLRecommendedNodeData{RecommendedNodeName: ""})
		return framework.NewStatus(framework.Error, "DRL server request failed, check Python server logs.")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		klog.Errorf("DRLScorer: Python server returned error %d for pod %s/%s: %s", resp.StatusCode, pod.Namespace, pod.Name, string(bodyBytes))
		cycleState.Write(DRLPreScoreStateKey, &DRLRecommendedNodeData{RecommendedNodeName: ""})
		return framework.NewStatus(framework.Error, fmt.Sprintf("DRL server error: %d", resp.StatusCode))
	}

	// Define a struct to match the expected JSON response
	type DRLResponse struct {
		RecommendedNode string `json:"recommended_node"`
		// Include other fields if you need them from the response
		// PodCPU         int    `json:"pod_cpu"`
		// PodRAM         int    `json:"pod_ram"`
		// NumCandidates  int    `json:"num_candidates"`
	}

	var responseData DRLResponse
	if err := json.NewDecoder(resp.Body).Decode(&responseData); err != nil {
		klog.Errorf("DRLScorer: Error decoding Python server response for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		cycleState.Write(DRLPreScoreStateKey, &DRLRecommendedNodeData{RecommendedNodeName: ""})
		return framework.NewStatus(framework.Error, "Failed to decode DRL response.")
	}

	recommendedNode := responseData.RecommendedNode
	if recommendedNode == "" {
		klog.Warningf("DRLScorer: Python server did not recommend a node for pod %s/%s. Response: %+v", pod.Namespace, pod.Name, responseData)
		// No specific node recommended, perhaps treat all as equal or rely on other scorers
		cycleState.Write(DRLPreScoreStateKey, &DRLRecommendedNodeData{RecommendedNodeName: ""})
	} else {
		klog.V(3).Infof("DRLScorer: Python server recommended node '%s' for pod %s/%s.", recommendedNode, pod.Namespace, pod.Name)
		cycleState.Write(DRLPreScoreStateKey, &DRLRecommendedNodeData{
			RecommendedNodeName: recommendedNode,
			PodName:             pod.Name,
			PodNamespace:        pod.Namespace,
		})
	}
	return framework.NewStatus(framework.Success)
}

// Score assigns a high score to the DRL-recommended node and a low score to others.
func (dsp *DRLScorerPlugin) Score(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	data, err := cycleState.Read(DRLPreScoreStateKey)
	if err != nil {
		klog.Errorf("DRLScorer: Error reading DRLRecommendedNodeState from cycle state for pod %s/%s, node %s: %v", pod.Namespace, pod.Name, nodeName, err)
		return framework.MinNodeScore, framework.AsStatus(fmt.Errorf("reading DRLRecommendedNodeState: %w", err))
	}

	recommendationData, ok := data.(*DRLRecommendedNodeData)
	if !ok || recommendationData == nil {
		klog.Errorf("DRLScorer: Invalid or nil data for DRLRecommendedNodeState for pod %s/%s, node %s", pod.Namespace, pod.Name, nodeName)
		return framework.MinNodeScore, framework.AsStatus(fmt.Errorf("invalid or nil data for DRLRecommendedNodeState"))
	}

	// Check if the pod matches the one for which the recommendation was made
	// This is a safeguard, as CycleState is per scheduling cycle (per pod).
	if recommendationData.PodName != "" && (pod.Name != recommendationData.PodName || pod.Namespace != recommendationData.PodNamespace) {
		klog.Warningf("DRLScorer: Mismatch between current pod (%s/%s) and pod in recommendation data (%s/%s) for node %s. This should not happen. Assigning MinScore.",
			pod.Namespace, pod.Name, recommendationData.PodNamespace, recommendationData.PodName, nodeName)
		return framework.MinNodeScore, framework.NewStatus(framework.Success)
	}

	if recommendationData.RecommendedNodeName == "" {
		// No specific recommendation was made, or an error occurred in PreScore.
		// Give a neutral or minimal score to allow other plugins to decide.
		klog.V(4).Infof("DRLScorer: No specific DRL recommendation for pod %s/%s. Node %s gets MinNodeScore from DRL.", pod.Namespace, pod.Name, nodeName)
		return framework.MinNodeScore, framework.NewStatus(framework.Success)
	}

	if nodeName == recommendationData.RecommendedNodeName {
		klog.V(4).Infof("DRLScorer: Node %s is recommended for pod %s/%s. Score: %d", nodeName, pod.Namespace, pod.Name, framework.MaxNodeScore)
		return framework.MaxNodeScore, framework.NewStatus(framework.Success)
	}

	klog.V(5).Infof("DRLScorer: Node %s is NOT recommended for pod %s/%s. Score: %d", nodeName, pod.Namespace, pod.Name, framework.MinNodeScore)
	return framework.MinNodeScore, framework.NewStatus(framework.Success)
}

// ScoreExtensions returns nil.
func (dsp *DRLScorerPlugin) ScoreExtensions() framework.ScoreExtensions {
	return nil
}
