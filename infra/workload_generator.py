import csv
import subprocess
import yaml
import argparse

def generate_workload_from_csv(row):
    # Create Pod spec with usage annotations for KWOK based on CSV data
    cpu = row['cpu usage']
    memory = row['memory usage']

    return {
        "apiVersion": "v1",
        "kind": "Pod",
        "metadata": {
            "name": row['name'],
            "annotations": {
                "kwok.x-k8s.io/usage-cpu": cpu,
                "kwok.x-k8s.io/usage-memory": memory
            }
        },
        "spec": {
            "containers": [{
                "name": "workload-container",
                "image": "busybox",  # Simple image for testing
                "resources": {
                    "requests": {
                        "cpu": cpu,
                        "memory": memory
                    },
                    "limits": {
                        "cpu": cpu,
                        "memory": memory
                    }
                }
            }]
        }
    }

def apply_workload_to_kwok(workload):
    command = ["kubectl", "apply", "-f", "-"]
    subprocess.run(command, input=yaml.dump(workload), text=True, check=True)

def main():
    # Set up command-line arguments
    parser = argparse.ArgumentParser(description="Generate KWOK workloads from a CSV file.")
    parser.add_argument("csv_file", type=str, help="Path to the CSV file with pod specs.")
    
    # Parse arguments
    args = parser.parse_args()

    # Read and process the CSV file
    with open(args.csv_file, newline='') as csvfile:
        reader = csv.DictReader(csvfile)
        for row in reader:
            workload = generate_workload_from_csv(row)
            apply_workload_to_kwok(workload)
            print(f"Applied: {workload['metadata']['name']}")

if __name__ == "__main__":
    main()
