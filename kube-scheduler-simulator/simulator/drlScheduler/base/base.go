package base

import (
	"encoding/json"
	"fmt"
	"os"
)

// NodeResourceInfo holds resource information for a node
type NodeResourceInfo struct {
	NodeName        string `json:"nodeName"`
	CPUTotal        int64  `json:"cpuTotal"`        // in millicores
	CPURemaining    int64  `json:"cpuRemaining"`    // in millicores
	CPUUsedPct      int64  `json:"cpuUsedPct"`      // in percentage
	CPUModel        string `json:"cpuModel"`        // CPU model name
	CPUFreq         int64  `json:"cpuFreq"`         // CPU frequency in MHz
	MemoryTotal     int64  `json:"memoryTotal"`     // in bytes
	MemoryRemaining int64  `json:"memoryRemaining"` // in bytes
	MemUsedPct      int64  `json:"memUsedPct"`      // in percentage
}

func SearchJSONKey(filePath, key string) (interface{}, error) {
	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("JSON file not found: %s", filePath)
	}

	// Read the JSON file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading file %s: %v", filePath, err)
	}

	// Parse JSON into a map
	var jsonData map[string]interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return nil, fmt.Errorf("invalid JSON in file %s: %v", filePath, err)
	}

	// Search for the key
	value, exists := jsonData[key]
	if !exists {
		return nil, nil // Key not found, return nil (equivalent to None in Python)
	}

	return value, nil
}

func CleanCPUModelName(cpuModel string) string {
	// Remove the dashes from the CPU model name and replace with spaces
	cleanedModel := ""
	for _, char := range cpuModel {
		if char != '-' {
			cleanedModel += string(char)
		} else {
			cleanedModel += " "
		}
	}
	return cleanedModel
}
