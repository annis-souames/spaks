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

func ConvertCPUModel(cpuModel string) float32 {
	// Logic to read from mean target encoding json file and return a mean
}

func GetEnergyScore(model goscore.RandomForest, nodeInfo NodeResourceInfo) (float64, error) {
	// Create features map for prediction
	features := map[string]interface{}{
		"CPU_Model":         nodeInfo.CPUModel,
		"RAM_Capacity_GB":   nodeInfo.MemoryTotal,
		"CPU_Freq_MHz":      nodeInfo.CPUFreq,
		"Num_Cores":         nodeInfo.CPUTotal,
		"Achieved_Load_Pct": nodeInfo.CPUUsedPct,
	}

	// Get prediction from model
	prediction, err := model.Score(features, "1")
	if err != nil {
		return 0, err
	}

	return prediction, nil
}
