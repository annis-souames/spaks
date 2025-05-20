package drlScheduler

import (
	"encoding/xml"
	"os"

	"github.com/asafschers/goscore"
)

func LoadEnergyModel(modelPath string) (goscore.RandomForest, error) {
	modelXml, _ := os.ReadFile("fixtures/model.pmml")
	var model goscore.RandomForest // or goscore.GradientBoostedModel
	xml.Unmarshal([]byte(modelXml), &model)
	return model, nil
}

func GetEnergyScore(model goscore.RandomForest, nodeInfo NodeResourceInfo) (float64, error) {
	// Create features map for prediction
	features := make(map[string]float64)

	// Convert resource metrics to features expected by model
	features["cpu_total"] = float64(nodeInfo.CPUTotal)
	features["cpu_remaining"] = float64(nodeInfo.CPURemaining)
	features["memory_total"] = float64(nodeInfo.MemoryTotal)
	features["memory_remaining"] = float64(nodeInfo.MemoryRemaining)

	// Get prediction from model
	prediction, err := model.Predict(features)
	if err != nil {
		return 0, err
	}

	return prediction, nil
}
