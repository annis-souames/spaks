package logScheduler

import (
	"context"
	"encoding/csv"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	framework "k8s.io/kubernetes/pkg/scheduler/framework"

	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/base"
	"sigs.k8s.io/kube-scheduler-simulator/simulator/drlScheduler/energy"
)

const (
	stateLogFilePath  = "states.csv"  // For detailed state
	actionLogFilePath = "actions.csv" // For chosen (pod, node) pairs
	cpuModelUnknown   = "Unknown"
	nodeTypeLabelKey  = "type"
	nodeTypeCloud     = "cloud"
	nodeTypeEdge      = "edge"
)

var (
	// For state logging (log_default_state.csv)
	csvWriterStateLog  *csv.Writer
	logFileStateLog    *os.File
	logMutexStateLog   sync.Mutex
	csvHeadersStateLog = []string{
		"timestamp",
		"pod_name",
		"pod_cpu_request_milli", "pod_ram_request_mib", // Sum of all containers
		"candidate_node_name",
		"node_cpu_total_milli",
		"node_ram_total_mib",
		"node_cpu_model_energy_val", // Numerical encoding of CPU model
		"node_type",                 // "cloud", "edge", or "unknown"
		"energy_before",
		"energy_after",
	}

	// For action logging (actions.csv)
	csvWriterActions  *csv.Writer
	logFileActions    *os.File
	logMutexActions   sync.Mutex
	csvHeadersActions = []string{
		"timestamp",
		"pod_name",
		"chosen_node_name",
	}
)

const (
	Name = "Logger"
)

// SchedulerLoggerPlugin is a plugin that logs data for  training
type SchedulerLoggerPlugin struct {
	handle framework.Handle
}

var _ framework.ScorePlugin = &SchedulerLoggerPlugin{}
var _ framework.PostBindPlugin = &SchedulerLoggerPlugin{}

// Name is the name of the plugin.
func (lp *SchedulerLoggerPlugin) Name() string {
	return Name
}

func initSpecificLogger(filePath string, headers []string, writer **csv.Writer, file **os.File, mutex *sync.Mutex, loggerName string) error {
	mutex.Lock()
	defer mutex.Unlock()

	var err error
	fileExists := false
	if _, errStat := os.Stat(filePath); errStat == nil {
		fileExists = true
	}

	*file, err = os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("%s: failed to open log file %s: %w", loggerName, filePath, err)
	}
	*writer = csv.NewWriter(*file)

	if !fileExists {
		if err := (*writer).Write(headers); err != nil {
			(*file).Close()
			return fmt.Errorf("%s: failed to write CSV header to %s: %w", loggerName, filePath, err)
		}
		(*writer).Flush()
	}
	klog.Infof("%s Plugin initialized. Logging to %s", loggerName, filePath)
	return nil
}

// NewSchedLogger initializes a new plugin and returns it.
func New(_ context.Context, _ runtime.Object, h framework.Handle) (framework.Plugin, error) {
	if err := initSpecificLogger(stateLogFilePath, csvHeadersStateLog, &csvWriterStateLog, &logFileStateLog, &logMutexStateLog, "StateLogger"); err != nil {
		return nil, err
	}
	if err := initSpecificLogger(actionLogFilePath, csvHeadersActions, &csvWriterActions, &logFileActions, &logMutexActions, "ActionLogger"); err != nil {
		// Attempt to close the state logger if action logger init fails
		CloseSchedLogger() // This will attempt to close stateLogFile if it was opened
		return nil, err
	}

	return &SchedulerLoggerPlugin{
		handle: h,
	}, nil
}

func (lp *SchedulerLoggerPlugin) Clone() framework.Plugin {
	klog.V(4).Infof("Logger Plugin: Clone() called.")
	// Create a new instance of the plugin.
	// The framework handle is passed around and is safe to reuse.
	// The logging resources (csvWriters, logFiles, mutexes) are global
	// and are intended to be shared across all "instances" or "clones"
	// of this logger, as they all write to the same log files.
	return &SchedulerLoggerPlugin{
		handle: lp.handle, // Share the same framework handle
	}
}

func closeSpecificLogger(writer **csv.Writer, file **os.File, mutex *sync.Mutex, loggerName string) {
	mutex.Lock()
	defer mutex.Unlock()
	if *writer != nil {
		(*writer).Flush()
		if err := (*writer).Error(); err != nil {
			klog.Errorf("%s: error flushing CSV writer: %v", loggerName, err)
		}
	}
	if *file != nil {
		if err := (*file).Close(); err != nil {
			klog.Errorf("%s: error closing log file: %v", loggerName, err)
		}
		*file = nil // Mark as closed
	}
}

// CloseSchedLogger ensures data is flushed and log files are closed.
func CloseSchedLogger() {
	closeSpecificLogger(&csvWriterStateLog, &logFileStateLog, &logMutexStateLog, "StateLogger")
	closeSpecificLogger(&csvWriterActions, &logFileActions, &logMutexActions, "ActionLogger")
	klog.Info("Logger Plugin (State & Actions) shut down.")
}

func (lp *SchedulerLoggerPlugin) getCPUEnergyValue(node *v1.Node) float64 {
	// Ensure you have a label like "cpu-model" on your nodes or adjust this.
	cpuModel := base.CleanCPUModelName(node.Labels["cpu-model"])
	if cpuModel == "" {
		cpuModel = cpuModelUnknown
	}

	// Assuming SearchJSONKey returns interface{} and needs type assertion
	cpuModelEnergyValInterface, err := base.SearchJSONKey("models/cpu_model.json", cpuModel)
	if err != nil {
		klog.Warningf("Logger: Error encoding CPU model '%s' for node %s: %v. Using default energy value.", cpuModel, node.Name, err)
		return 200.0 // Default or error indicator value
	}

	cpuModelEnergyVal, ok := cpuModelEnergyValInterface.(float64)
	if !ok {
		klog.Warningf("Logger: CPU model energy value for '%s' (node %s) is not a float64: %T. Using default.", cpuModel, node.Name, cpuModelEnergyValInterface)
		return 200.0 // Default or error indicator value
	}
	return cpuModelEnergyVal
}

func (lp *SchedulerLoggerPlugin) getPodResourceRequests(pod *v1.Pod) (cpuMillis int64, memMib int64) {
	for _, container := range pod.Spec.Containers {
		if container.Resources.Requests != nil {
			cpuMillis += container.Resources.Requests.Cpu().MilliValue()
			memMib += container.Resources.Requests.Memory().Value()
		}
	}
	return
}

func (lp *SchedulerLoggerPlugin) getNodeResourceInfo(nodeInfo *framework.NodeInfo, podForHypotheticalPlacement *v1.Pod) (current base.NodeResourceInfo, hypotheticalAfterPlacement base.NodeResourceInfo) {
	node := nodeInfo.Node()
	if node == nil {
		klog.Warningf("getNodeResourceInfo called with nil node for NodeInfo of %s", nodeInfo.Node().Name) // Assuming nodeInfo.Node() wouldn't be nil if node is
		return                                                                                             // Return empty structs
	}

	// Capacities
	cpuCapacity := node.Status.Capacity.Cpu().MilliValue()
	memCapacityBytes := node.Status.Capacity.Memory().Value()

	// Current total requested resources by existing pods on the node
	currentCpuUsed := nodeInfo.Requested.MilliCPU
	currentMemUsedBytes := nodeInfo.Requested.Memory

	// Current state
	current.NodeName = node.Name
	current.CPUModel = base.CleanCPUModelName(node.Labels["cpu-model"]) // Ensure label key is correct
	current.CPUFreq = rand.Int63n(3200-2600) + 2600                     // Example
	current.CPUTotal = cpuCapacity
	if cpuCapacity > 0 {
		current.CPUUsedPct = (currentCpuUsed * 100) / cpuCapacity
	} else {
		current.CPUUsedPct = 0
	}
	current.MemoryTotal = memCapacityBytes
	if memCapacityBytes > 0 {
		current.MemUsedPct = (currentMemUsedBytes * 100) / memCapacityBytes
	} else {
		current.MemUsedPct = 0
	}

	// Hypothetical state if podForHypotheticalPlacement is added
	hypotheticalAfterPlacement = current // Start with current state
	podCPUReq, podMemReqBytes := int64(0), int64(0)
	if podForHypotheticalPlacement != nil {
		tempPodCpu, tempPodMemMib := lp.getPodResourceRequests(podForHypotheticalPlacement)
		podCPUReq = tempPodCpu
		podMemReqBytes = tempPodMemMib * 1024 * 1024
	}

	hypotheticalCpuUsed := currentCpuUsed + podCPUReq
	hypotheticalMemUsedBytes := currentMemUsedBytes + podMemReqBytes

	if cpuCapacity > 0 {
		hypotheticalAfterPlacement.CPUUsedPct = (hypotheticalCpuUsed * 100) / cpuCapacity
	} else {
		hypotheticalAfterPlacement.CPUUsedPct = 0
	}
	if memCapacityBytes > 0 {
		hypotheticalAfterPlacement.MemUsedPct = (hypotheticalMemUsedBytes * 100) / memCapacityBytes
	} else {
		hypotheticalAfterPlacement.MemUsedPct = 0
	}
	// Other fields like CPUTotal, MemoryTotal, CPUModel, CPUFreq remain the same for hypothetical.

	// klog.V(5).Infof("Node %s: Current CPU Used Pct: %d, Hypo CPU Used Pct: %d", node.Name, current.CPUUsedPct, hypotheticalAfterPlacement.CPUUsedPct)
	return
}

// Score logs the state of each candidate node.
func (lp *SchedulerLoggerPlugin) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	nodeInfo, err := lp.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		klog.Errorf("Logger: Error getting node info for %s in Score: %v", nodeName, err)
		return 0, framework.AsStatus(fmt.Errorf("getting node %s from Snapshot: %w", nodeName, err))
	}
	if nodeInfo.Node() == nil {
		klog.Errorf("Logger: Node %s not found in NodeInfo in Score", nodeName)
		return 0, framework.AsStatus(fmt.Errorf("node %s not found in NodeInfo for Score", nodeName))
	}

	resourceCurrent, resourceHypotheticalAfterPod := lp.getNodeResourceInfo(nodeInfo, pod)

	energyBefore, err := energy.PredictEnergyConsumption(&resourceCurrent, nodeName)
	if err != nil {
		klog.Warningf("Logger: Error predicting energy (before) for node %s, pod %s: %v", nodeName, pod.Name, err)
		energyBefore = -1.0 // Indicate error or use a default
	}

	energyAfter, err := energy.PredictEnergyConsumption(&resourceHypotheticalAfterPod, nodeName)
	if err != nil {
		klog.Warningf("Logger: Error predicting energy (after hypothetical) for node %s, pod %s: %v", nodeName, pod.Name, err)
		energyAfter = -1.0 // Indicate error or use a default
	}

	var nodeTypeStr string
	if strings.Contains(strings.ToLower(nodeName), "cloud") {
		nodeTypeStr = nodeTypeCloud
	} else if strings.Contains(strings.ToLower(nodeName), "edge") {
		nodeTypeStr = nodeTypeEdge
	} else {
		nodeTypeStr = "unknown"
	}

	podCPUReqMilli, podRAMReqMib := lp.getPodResourceRequests(pod)

	logEntry := []string{
		time.Now().Format(time.RFC3339Nano),
		pod.Name,
		strconv.FormatInt(podCPUReqMilli, 10),
		strconv.FormatInt(podRAMReqMib, 10),
		nodeName, // candidate_node_name
		strconv.FormatInt(resourceCurrent.CPUTotal, 10),
		strconv.FormatInt(resourceCurrent.MemoryTotal/(1024*1024), 10), // MiB
		fmt.Sprintf("%.4f", lp.getCPUEnergyValue(nodeInfo.Node())),
		nodeTypeStr,
		fmt.Sprintf("%.4f", energyBefore),
		fmt.Sprintf("%.4f", energyAfter),
	}

	logMutexStateLog.Lock()
	if csvWriterStateLog != nil {
		if err := csvWriterStateLog.Write(logEntry); err != nil {
			klog.Errorf("Logger: Failed to write state log for pod %s, node %s: %v", pod.Name, nodeName, err)
		}
		csvWriterStateLog.Flush()
	}
	logMutexStateLog.Unlock()

	return 0, framework.NewStatus(framework.Success)
}

// ScoreExtensions returns nil as this plugin does not rank nodes.
func (lp *SchedulerLoggerPlugin) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

// PostBind is called after a pod is successfully bound to a node.
// It logs the chosen action (pod_name, chosen_node_name).
func (lp *SchedulerLoggerPlugin) PostBind(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) {
	logEntry := []string{
		time.Now().Format(time.RFC3339Nano),
		pod.Name,
		nodeName, // chosen_node_name
	}

	logMutexActions.Lock()
	if csvWriterActions != nil {
		if err := csvWriterActions.Write(logEntry); err != nil {
			klog.Errorf("Logger: Failed to write action log for pod %s, node %s: %v", pod.Name, nodeName, err)
		}
		csvWriterActions.Flush()
	}
	logMutexActions.Unlock()

	klog.V(4).Infof("Logger: Pod %s/%s bound to node %s. Action logged.", pod.Namespace, pod.Name, nodeName)
}
