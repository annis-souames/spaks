from flask import Flask, request, jsonify
import torch
import torch.nn as nn
import torch.nn.functional as F
import numpy as np
import pickle
import os

# --- Configuration ---
MODEL_PATH = "models/dqn_model.pth" # Path to your saved best model
SCALER_PATH = "models/scaler.pkl"     # Path to your saved scaler
N_OBSERVATIONS = 7 # Must match the number of features your model was trained on

# --- Define the DQN Model Class (must be identical to the one used for training) ---
class DQN(nn.Module):
    def __init__(self, n_observations):
        super(DQN, self).__init__()
        self.layer1 = nn.Linear(n_observations, 128)
        self.layer2 = nn.Linear(128, 128)
        self.layer3 = nn.Linear(128, 1)

    def forward(self, x):
        x = F.relu(self.layer1(x))
        x = F.relu(self.layer2(x))
        return self.layer3(x)

# --- Global Variables for Model and Scaler ---
inference_model = None
feature_scaler = None
device = torch.device("cuda" if torch.cuda.is_available() else "cpu")

def encode_node_type_server(node_type_str: str) -> float:
    """Encodes node type string to float. Must match training encoding."""
    if node_type_str == 'edge':
        return 1.0
    elif node_type_str == 'cloud':
        return 2.0
    else: # unknown or other
        return 0.0

def load_model_and_scaler():
    global inference_model, feature_scaler
    print(f"Attempting to load model from: {os.path.abspath(MODEL_PATH)}")
    print(f"Attempting to load scaler from: {os.path.abspath(SCALER_PATH)}")

    if not os.path.exists(MODEL_PATH):
        raise FileNotFoundError(f"Model file not found: {MODEL_PATH}")
    if not os.path.exists(SCALER_PATH):
        raise FileNotFoundError(f"Scaler file not found: {SCALER_PATH}")

    try:
        inference_model = DQN(N_OBSERVATIONS).to(device)
        # Load model weights, ensuring map_location handles CPU/GPU differences
        inference_model.load_state_dict(torch.load(MODEL_PATH, map_location=device))
        inference_model.eval() # Set to evaluation mode
        print("DQN model loaded successfully.")
    except Exception as e:
        print(f"Error loading DQN model: {e}")
        raise

    try:
        with open(SCALER_PATH, 'rb') as f:
            feature_scaler = pickle.load(f)
        print("Feature scaler loaded successfully.")
    except Exception as e:
        print(f"Error loading feature scaler: {e}")
        raise

# --- Flask App ---
app = Flask(__name__)

@app.route('/predict_node_scores', methods=['POST'])
def predict_node_scores():
    global inference_model, feature_scaler

    if inference_model is None or feature_scaler is None:
        return jsonify({"error": "Model or scaler not loaded"}), 500

    try:
        data = request.get_json()
        print(f"Received data: {data}")
        if not data:
            return jsonify({"error": "No data provided"}), 400

        pod_info = data.get('pod_info')
        candidate_nodes_info = data.get('candidate_nodes') # List of node dicts

        if not pod_info or not candidate_nodes_info:
            return jsonify({"error": "Missing 'pod_info' or 'candidate_nodes'"}), 400
        
        pod_cpu = pod_info.get('pod_cpu_request')
        pod_ram = pod_info.get('pod_ram_request_mib')

        if pod_cpu is None or pod_ram is None:
            return jsonify({"error": "Missing pod CPU or RAM requests in 'pod_info'"}), 400

        node_scores = {}

        for node_data in candidate_nodes_info:
            node_name = node_data.get('candidate_node_name')
            if not node_name:
                print("Warning: Skipping node due to missing 'candidate_node_name'")
                continue
            
            # Construct state vector for this pod-node pair
            # Ensure the order and features exactly match what the model was trained on
            try:
                s_t_raw = np.array([
                    pod_cpu,
                    pod_ram,
                    node_data['node_cpu_total'],
                    node_data['node_ram_total_mib'],
                    node_data['node_cpu_model_energy_val'],
                    encode_node_type_server(node_data['node_type']), # Use server-side encoding
                    node_data['energy_before_pod_placement_on_node'] # This is crucial
                ], dtype=np.float32)
            except KeyError as e:
                print(f"Warning: Missing feature for node {node_name}: {e}. Skipping node.")
                node_scores[node_name] = -float('inf') # Penalize heavily if data is missing
                continue

            # Scale the state vector
            s_t_scaled = feature_scaler.transform(s_t_raw.reshape(1, -1))[0]
            state_tensor = torch.tensor(s_t_scaled, device=device, dtype=torch.float32).unsqueeze(0)

            with torch.no_grad():
                predicted_q_value = inference_model(state_tensor).item()
            
            node_scores[node_name] = predicted_q_value
            # print(f"Node: {node_name}, Raw State: {s_t_raw}, Scaled: {s_t_scaled}, Q-Value: {predicted_q_value}")

        print(f"Node scores computed: {node_scores}")
        return jsonify({"node_scores": node_scores}), 200

    except Exception as e:
        print(f"Error during prediction: {e}")
        import traceback
        traceback.print_exc()
        return jsonify({"error": f"Internal server error: {str(e)}"}), 500

if __name__ == '__main__':
    try:
        load_model_and_scaler()
        print(f"Flask server starting on port 5001...")
        app.run(host='0.0.0.0', port=5001, debug=False) # Set debug=False for production-like
    except FileNotFoundError as e:
        print(f"CRITICAL ERROR: Could not load model or scaler. Server not started. {e}")
    except Exception as e:
        print(f"CRITICAL ERROR starting server: {e}")