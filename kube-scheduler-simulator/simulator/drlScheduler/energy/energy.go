package energy

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/dmitryikh/leaves"
	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/base"
)

const (
	MAX_ENERGY = 2800 // Maximum energy consumption value for normalization
)

func loadModel() (*leaves.Ensemble, error) {
	// 1. Read model
	useTransformation := true
	model, err := leaves.LGEnsembleFromFile("models/lgbm.txt", useTransformation)
	if err != nil {
		return nil, fmt.Errorf("failed to load model: %w", err)
	}

	return model, nil
}

// calculateNetworkCost compute an energy additional cost based if the node is in the cloud or edge: cloud nodes are more expensive while edge nodes are less expensive
func calculateNetworkCost(nodeName string) float64 {
	// Check if the node name conatisn the word "cloud"
	if strings.Contains(strings.ToLower(nodeName), "cloud") {
		// Generate an energy between 60 to 100
		return float64(rand.IntN(100-60) + 60) // Cloud nodes have a higher cost
	} else {
		// Generate an energy between 20 to 50
		return float64(rand.IntN(50-20) + 20) // Edge nodes have a lower cost
	}

}

// getCPUModelEncoding provides a mock implementation for encoding CPU models.
// Replace this with the actual logic for encoding CPU models.
func GetCPUModelEncoding(cpuModel string) float64 {
	cpuAvg, err := base.SearchJSONKey("models/cpu_models.json", base.CleanCPUModelName(cpuModel))
	if err != nil && strings.Contains(cpuModel, "cloud") {
		// If the CPU model is not found in the JSON file, return a default value for cloud nodes
		cpuAvg = 200.10
	}
	if err != nil && strings.Contains(cpuModel, "edge") {
		// If the CPU model is not found in the JSON file, return a default value for edge nodes
		cpuAvg = 50.50
	}
	return cpuAvg.(float64)
}

// extractFeatures extracts features from the cluster state for the given node and convert it into a format for leaves
func extractFeatures(nodeState *base.NodeResourceInfo, nodeName string) ([]float64, error) {
	// Get the target encoding of the cpu model
	cpuAvg := GetCPUModelEncoding(nodeState.CPUModel)

	fmt.Printf(">>>> CPU model: %s, encoding: %v\n", nodeState.CPUModel, cpuAvg)

	features := []float64{
		cpuAvg, // CPU model encoding
		float64(nodeState.MemoryTotal),
		float64(nodeState.CPUFreq), // CPU frequency in MHz
		float64(nodeState.CPUTotal),
		float64(nodeState.CPUUsedPct),
	}

	fmt.Printf(">>>> Extracted features for node %s: %v\n", nodeName, features)

	return features, nil
}

func PredictEnergyConsumption(nodeState *base.NodeResourceInfo, nodeName string) (float64, error) {
	// Placeholder for energy consumption prediction logic
	// This function should use the clusterState and nodeName to predict energy consumption
	// For now, we return a random value as a placeholder

	model, err := loadModel()
	if err != nil {
		return 0, fmt.Errorf("failed to load model: %w", err)
	}

	features, err := extractFeatures(nodeState, nodeName)
	if err != nil {
		return 0, fmt.Errorf("failed to extract features: %w", err)
	}

	energyVal := model.PredictSingle(features, 0) + calculateNetworkCost(nodeName)

	fmt.Printf(">>>>energy consumption for node %s: %.2f Watts | Network Cost: %.2f \n", nodeName, energyVal, calculateNetworkCost(nodeName))

	return energyVal, nil
}
