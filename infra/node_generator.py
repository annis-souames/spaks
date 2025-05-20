#!/usr/bin/env python3
"""
Apply Kubernetes Node resources to a KWOK-backed cluster based on specs in a CSV file.

CSV format:
  name,labels,cpu,memory,cpu_model[,pods]

- name: node name
- labels: semicolon-separated key=value pairs
- cpu: number or value with unit (e.g., 4, "4", "4m", etc.)
- memory: value with unit (e.g., "8Gi")
- cpu_model: CPU model name (e.g., "Intel Xeon E5-2680")
- pods: (optional) maximum pods, defaults to 110

Example CSV:
name,labels,cpu,memory,cpu_model,pods
kwok-node-0,"beta.kubernetes.io/os=linux;kubernetes.io/role=agent",32,256Gi,"Intel Xeon E5-2680",110
kwok-node-1,"beta.kubernetes.io/os=linux;kubernetes.io/role=agent",16,128Gi,"AMD EPYC 7763",100

Usage:
  pip install pyyaml
  python apply_nodes_from_csv.py nodes.csv --server=:3131
"""
import csv
import subprocess
import argparse

try:
    import yaml
except ImportError:
    print("ERROR: PyYAML not found. Install with: pip install pyyaml")
    exit(1)


def parse_labels(labels_str):
    """
    Parse semicolon-separated key=value pairs into a dict.
    """
    labels = {}
    for part in labels_str.split(';'):
        if not part.strip():
            continue
        key, val = part.split('=', 1)
        labels[key.strip()] = val.strip()
    return labels


def build_node_manifest(name, labels, cpu, memory, pods):
    """
    Construct a Node manifest dict for kubectl.
    """
    # Add cpu_model to labels if provided
    if 'cpu_model' in labels:
        labels['cpu-model'] = labels.pop('cpu_model')
        
    return {
        'apiVersion': 'v1',
        'kind': 'Node',
        'metadata': {
            'name': name,
            'annotations': {
                'node.alpha.kubernetes.io/ttl': '0',
                'kwok.x-k8s.io/node': 'fake',
            },
            'labels': labels,
        },
        'spec': {

        },
        'status': {
            'capacity': {
                'cpu': str(cpu),
                'memory': memory,
                'pods': "110"
            },
            'allocatable': {
                'cpu': str(cpu),
                'memory': memory,
                'pods': "110"
            },
            'nodeInfo': {
                'architecture': 'amd64',
                'bootID': '',
                'containerRuntimeVersion': '',
                'kernelVersion': '',
                'kubeProxyVersion': 'fake',
                'kubeletVersion': 'fake',
                'machineID': '',
                'operatingSystem': 'linux',
                'osImage': '',
                'systemUUID': '',
            },
            'phase': 'Running',
        }
    }


def main(csv_path):
    with open(csv_path) as f:
        reader = csv.DictReader(f)
        for row in reader:
            name = row['name']
            labels = parse_labels(row.get('labels', ''))
            # Add CPU model to labels if present
            if 'cpu_model' in row:
                labels['cpu-model'] = row['cpu_model']
            cpu = row['cpu']
            memory = row.get('memory') or row.get('ram')
            pods = int(row.get('pods', 110))
            manifest = build_node_manifest(name, labels, cpu, memory, pods)
            yaml_str = yaml.dump(manifest, default_flow_style=False)

            subprocess.run(
                ['kubectl', 'apply', '-f', '-'],
                input=yaml_str.encode(),
                check=True
            )
            print(f"Applied Node: {name}")


if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description='Apply Nodes to KWOK cluster from CSV file'
    )
    parser.add_argument('csv_file', help='Path to CSV file')
    args = parser.parse_args()
    main(args.csv_file)
