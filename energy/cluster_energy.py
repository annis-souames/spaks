"""
This script compute the energy consumption of a cluster based on the node load using the trained LGBM model.
"""

import numpy as np
import lightgbm as lgb
import os
import csv
import subprocess
import json
import re
import random

# Read the nodes from ../infra/resources/nodes.csv
def getNodes():
    nodes = []
    with open('../infra/resources/nodes.csv', 'r') as f:
        reader = csv.DictReader(f)
        for row in reader:
            nodes.append(row)
    return nodes

# Read the workloads from ../infra/resources/workloads.csv
def getWorkloads():
    workloads = []
    with open('../infra/resources/workloads.csv', 'r') as f:
        reader = csv.DictReader(f)
        for row in reader:
            workloads.append(row)
    return workloads

def load_lgbm_model():
    """
    Load the trained LightGBM model from the txt file.
    """
    try:
        model = lgb.Booster(model_file='pickle/lgbm.txt')
        print("LightGBM model loaded successfully")
        return model
    except Exception as e:
        print(f"Error loading LightGBM model: {e}")
        return None

def load_cpu_model_encoding():
    """
    Load the CPU model mean target encoding from JSON file.
    """
    try:
        with open('cpu_models.json', 'r') as f:
            encoding = json.load(f)
        print("CPU model encoding loaded successfully")
        return encoding
    except Exception as e:
        print(f"Error loading CPU model encoding: {e}")
        return {}

def get_k8s_nodes():
    """
    Get all nodes in the Kubernetes cluster using kubectl.
    Returns a list of node objects with their names, capacities, and hardware specs.
    """
    try:
        # Get nodes with their capacities in JSON format
        cmd = ["kubectl", "get", "nodes", "-o", "json"]
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        nodes_data = json.loads(result.stdout)
        
        nodes = []
        for node in nodes_data.get('items', []):
            node_name = node['metadata']['name']
            labels = node['metadata'].get('labels', {})
            
            # Get CPU capacity (in millicores)
            cpu_capacity = node['status']['capacity'].get('cpu', '0')
            cpu_capacity_millicores = parse_cpu_to_millicores(cpu_capacity)
            
            # Get memory capacity (convert from Ki to GB)
            memory_capacity = node['status']['capacity'].get('memory', '0Ki')
            memory_gb = parse_memory_to_gb(memory_capacity)
            
            # Get CPU model from labels
            cpu_model = labels.get('cpu-model', 'unknown')
            
            # Extract CPU frequency and core count from node info
            # Note: These might need to be adjusted based on your cluster's label structure
            cpu_freq_mhz = extract_cpu_freq_from_labels(labels)
            num_cores = int(float(cpu_capacity))  # CPU capacity usually represents cores
            
            nodes.append({
                'name': node_name,
                'cpu_capacity': cpu_capacity_millicores,
                'cpu_model': cpu_model,
                'ram_capacity_gb': memory_gb,
                'cpu_freq_mhz': cpu_freq_mhz,
                'num_cores': num_cores
            })
        
        return nodes
    except subprocess.CalledProcessError as e:
        print(f"Error getting nodes: {e}")
        return []
    except json.JSONDecodeError as e:
        print(f"Error parsing kubectl output: {e}")
        return []

def parse_memory_to_gb(memory_str):
    """
    Convert memory string to GB.
    Examples: '7901Mi' -> ~7.7, '8000000Ki' -> ~7.8
    """
    if memory_str.endswith('Ki'):
        return int(memory_str[:-2]) / (1024 * 1024)
    elif memory_str.endswith('Mi'):
        return int(memory_str[:-2]) / 1024
    elif memory_str.endswith('Gi'):
        return int(memory_str[:-2])
    else:
        # Assume bytes
        return int(memory_str) / (1024**3)

def extract_cpu_freq_from_labels(labels):
    """
    Extract CPU frequency from node labels.
    This might need customization based on your cluster's labeling.
    """
    # Common label patterns for CPU frequency
    freq_labels = ['cpu-frequency', 'node.kubernetes.io/cpu-frequency', 'cpu-freq-mhz']
    
    for label in freq_labels:
        if label in labels:
            freq_str = labels[label]
            # Extract numeric value from frequency string
            freq_match = re.search(r'(\d+)', freq_str)
            if freq_match:
                return int(freq_match.group(1))
    
    # Default frequency if not found (you might want to adjust this)
    return 2400

def parse_cpu_to_millicores(cpu_str):
    """
    Convert CPU string to millicores.
    Examples: '2' -> 2000, '500m' -> 500, '1.5' -> 1500
    """
    if cpu_str.endswith('m'):
        return int(cpu_str[:-1])
    else:
        return int(float(cpu_str) * 1000)

def parse_cpu_request(cpu_request_str):
    """
    Parse CPU request string to millicores.
    Examples: '100m' -> 100, '0.1' -> 100, '1' -> 1000
    """
    if not cpu_request_str:
        return 0
    
    if cpu_request_str.endswith('m'):
        return int(cpu_request_str[:-1])
    else:
        return int(float(cpu_request_str) * 1000)

def get_pods_on_node(node_name: str):
    """
    Get all pods scheduled on a specific node using the annotation filter.
    Returns a list of pods with their CPU requests.
    """
    try:
        # Use kubectl to get pods with the specific annotation for this node
        annotation_filter = f"kube-scheduler-simulator.sigs.k8s.io/selected-node={node_name}"
        cmd = [
            "kubectl", "get", "pods", 
            "--all-namespaces",
            "-o", "json",
            "--field-selector", f"spec.nodeName={node_name}"
        ]
        
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        pods_data = json.loads(result.stdout)
        
        pods = []
        for pod in pods_data.get('items', []):
            pod_name = pod['metadata']['name']
            namespace = pod['metadata']['namespace']
            
            # Check if pod has the annotation (if you want to filter by annotation)
            annotations = pod['metadata'].get('annotations', {})
            has_annotation = annotation_filter.split('=')[0] in annotations
            
            # Calculate total CPU request for all containers in the pod
            total_cpu_request = 0
            containers = pod['spec'].get('containers', [])
            
            for container in containers:
                resources = container.get('resources', {})
                requests = resources.get('requests', {})
                cpu_request = requests.get('cpu', '0')
                total_cpu_request += parse_cpu_request(cpu_request)
            
            pods.append({
                'name': pod_name,
                'namespace': namespace,
                'cpu_request': total_cpu_request,
                'has_annotation': has_annotation
            })
        
        return pods
        
    except subprocess.CalledProcessError as e:
        print(f"Error getting pods for node {node_name}: {e}")
        return []
    except json.JSONDecodeError as e:
        print(f"Error parsing kubectl output for node {node_name}: {e}")
        return []

def getLoadOfNode(node_name: str, node_cpu_capacity: int):
    """
    Get the load of a node based on its name.
    The load is calculated as the sum of CPU requests of all pods running on the node
    divided by the node's CPU capacity.
    
    Args:
        node_name: Name of the node
        node_cpu_capacity: CPU capacity of the node in millicores
    
    Returns:
        float: Load percentage (0.0 to 1.0+)
    """
    pods = get_pods_on_node(node_name)
    
    total_cpu_request = 0
    pod_count = 0
    
    for pod in pods:
        total_cpu_request += pod['cpu_request']
        pod_count += 1
        print(f"  Pod: {pod['namespace']}/{pod['name']} - CPU Request: {pod['cpu_request']}m")
    
    if node_cpu_capacity == 0:
        print(f"Warning: Node {node_name} has zero CPU capacity")
        return 0.0
    
    load = total_cpu_request / node_cpu_capacity
    
    print(f"Node {node_name}:")
    print(f"  Total pods: {pod_count}")
    print(f"  Total CPU requests: {total_cpu_request}m")
    print(f"  Node CPU capacity: {node_cpu_capacity}m")
    print(f"  Load: {load:.2%}")
    print()
    
    return load

def get_network_cost(node_info):
    if 'cloud' in node_info["name"].lower():
        return random.randint(500, 1200) # Simulate higher costs for cloud nodes
    elif 'edge' in node_info["name"].lower():
        return random.randint(20, 70)

def predict_energy_consumption(node_info, load_pct, model, cpu_encoding):
    """
    Predict energy consumption for a node using the LightGBM model.
    
    Args:
        node_info: Dictionary containing node hardware information
        load_pct: Load percentage (0.0 to 1.0)
        model: Loaded LightGBM model
        cpu_encoding: CPU model encoding dictionary
    
    Returns:
        float: Predicted energy consumption in watts
    """
    if model is None:
        return 0.0
    
    if load_pct == 0.0:
        return 0.0  # No load means no energy consumption
    
    # Get encoded CPU model value
    cpu_model = node_info["cpu_model"].replace('-', ' ')
    cpu_model_encoded = cpu_encoding.get(cpu_model, None)
    
    # Prepare features in the correct order: 
    # ['CPU_Model', 'RAM_Capacity_GB', 'CPU_Freq_MHz', 'Num_Cores', 'Achieved_Load_Pct']
    features = np.array([[
        cpu_model_encoded,                           # CPU_Model (encoded)
        node_info.get('ram_capacity_gb', 8.0) * (1024*1024*1024),      # RAM_Capacity in bytes
        node_info.get('cpu_freq_mhz', 2400),        # CPU_Freq_MHz
        node_info.get('num_cores', 2) * 1000,              # Num_Cores
        load_pct * 100                              # Achieved_Load_Pct (convert to percentage)
    ]])
    print(f"Predicting energy consumption for node {node_info['name']} with features: {features[0]}")
    try:
        prediction = model.predict(features)[0]
        return max(0.0, prediction) + get_network_cost(node_info)  # Ensure non-negative energy consumption
    except Exception as e:
        print(f"Error predicting energy consumption: {e}")
        return 0.0

def measure_cluster_load():
    """
    Measure the load of all nodes in the cluster and predict energy consumption.
    """
    print("Loading LightGBM model and CPU encoding...")
    model = load_lgbm_model()
    cpu_encoding = load_cpu_model_encoding()
    
    print("Getting nodes from Kubernetes cluster...")
    nodes = get_k8s_nodes()
    
    if not nodes:
        print("No nodes found or error accessing cluster")
        return
    
    print(f"Found {len(nodes)} nodes in the cluster\n")
    
    cluster_results = []
    
    for node in nodes:
        node_name = node['name']
        node_cpu_capacity = node['cpu_capacity']
        
        print(f"Analyzing node: {node_name}")
        print(f"  CPU Model: {node['cpu_model']}")
        print(f"  RAM Capacity: {node['ram_capacity_gb']:.1f} GB")
        print(f"  CPU Frequency: {node['cpu_freq_mhz']} MHz")
        print(f"  Number of Cores: {node['num_cores']}")
        
        load = getLoadOfNode(node_name, node_cpu_capacity)
        
        # Predict energy consumption
        energy_watts = predict_energy_consumption(node, load, model, cpu_encoding)
        
        print(f"  Predicted Energy Consumption: {energy_watts:.2f} W")
        print("-" * 60)
        
        cluster_results.append({
            'node_name': node_name,
            'load': load,
            'cpu_capacity': node_cpu_capacity,
            'energy_watts': energy_watts,
            'node_info': node
        })
    
    # Print summary
    print("=" * 70)
    print("CLUSTER ENERGY CONSUMPTION SUMMARY")
    print("=" * 70)
    
    total_capacity = sum(node['cpu_capacity'] for node in cluster_results)
    weighted_load = sum(node['load'] * node['cpu_capacity'] for node in cluster_results) / total_capacity if total_capacity > 0 else 0
    total_energy = sum(node['energy_watts'] for node in cluster_results)
    
    print(f"{'Node Name':<25} {'Load':<10} {'Energy (W)':<12}")
    print("-" * 50)
    
    for node in cluster_results:
        print(f"{node['node_name']} {node['load']:.2f} {node['energy_watts']:.2f}")
    
    print("-" * 50)
    print(f"{'CLUSTER TOTALS':<25} {weighted_load:.2f} {total_energy:.2f}")
    print()
    print(f"Total Cluster Energy Consumption: {total_energy:.2f} W")
    print(f"Average Energy per Node: {total_energy/len(cluster_results):.2f} W")
    print(f"Cluster Average Load (weighted): {weighted_load:.2%}")
    
    # Additional insights
    if cluster_results:
        max_energy_node = max(cluster_results, key=lambda x: x['energy_watts'])
        min_energy_node = min(cluster_results, key=lambda x: x['energy_watts'])
        
        print(f"\nHighest Energy Consumer: {max_energy_node['node_name']} ({max_energy_node['energy_watts']:.2f} W)")
        print(f"Lowest Energy Consumer: {min_energy_node['node_name']} ({min_energy_node['energy_watts']:.2f} W)")
        
        # Calculate energy efficiency (watts per % load)
        efficiency_data = []
        for node in cluster_results:
            if node['load'] > 0:
                efficiency = node['energy_watts'] / (node['load'] * 100)
                efficiency_data.append((node['node_name'], efficiency))
        
        if efficiency_data:
            most_efficient = min(efficiency_data, key=lambda x: x[1])
            least_efficient = max(efficiency_data, key=lambda x: x[1])
            print(f"\nMost Energy Efficient: {most_efficient[0]} ({most_efficient[1]:.3f} W/%load)")
            print(f"Least Energy Efficient: {least_efficient[0]} ({least_efficient[1]:.3f} W/%load)")
    
    return cluster_results

if __name__ == "__main__":
    # Check if kubectl is available
    try:
        subprocess.run(["kubectl", "version", "--client"], capture_output=True, check=True)
        print("kubectl is available")
    except (subprocess.CalledProcessError, FileNotFoundError):
        print("Error: kubectl is not available or not configured properly")
        exit(1)
    
    # Check if model files exist
    if not os.path.exists('pickle/lgbm.txt'):
        print("Warning: LightGBM model file 'pickle/lgbm.txt' not found")
    
    if not os.path.exists('cpu_models.json'):
        print("Warning: CPU model encoding file 'cpu_models.json' not found")
    
    # Measure cluster load and energy consumption
    cluster_results = measure_cluster_load()