#!/usr/bin/env python3
"""
DQN Kubernetes Scheduler Inference Server
Provides REST API for Go scheduler to get node recommendations
"""

import torch
import numpy as np
import json
import logging
from flask import Flask, request, jsonify
from typing import List, Dict, Any, Optional
import traceback
from dataclasses import dataclass
import os

# Import your DQN model (assuming it's in the same directory)
from dqn import DQNScheduler

# Setup logging
logging.basicConfig(level=logging.INFO)

@dataclass
class PodRequest:
    """Pod resource requirements"""
    cpu_request_milli: int
    ram_request_mib: int

@dataclass 
class NodeCandidate:
    """Candidate node information"""
    name: str
    cpu_total_milli: int
    ram_total_mib: int
    cpu_model_energy_val: float
    node_type: str
    energy_before: float

class NodeTypeEncoder:
    """Encode node types to numerical values"""
    def __init__(self):
        self.type_mapping = {
            'cloud': 2,
            'edge': 1,
        }
    
    def encode(self, node_type: str) -> int:
        return self.type_mapping.get(node_type.lower(), 0)  # Default to cloud

class DQNInferenceServer:
    """DQN Model Inference Server"""
    
    def __init__(self, model_path: str, state_dim: int = 7):
        self.device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
        self.model = None
        self.node_encoder = NodeTypeEncoder()
        self.state_dim = state_dim

        self.logger = logging.getLogger(__name__)
        print(self.logger)
        
        # Load model
        self.load_model(model_path)
    
    def load_model(self, model_path: str):
        """Load the trained DQN model"""
        try:
            if not os.path.exists(model_path):
                raise FileNotFoundError(f"Model file not found: {model_path}")
            
            # Initialize model
            self.model = DQNScheduler(state_dim=self.state_dim).to(self.device)
            
            # Load checkpoint
            checkpoint = torch.load(model_path, map_location=self.device)
            self.model.load_state_dict(checkpoint['q_network_state'])
            self.model.eval()
            
            self.logger.info(f"Model loaded successfully from {model_path}")
            self.logger.info(f"Using device: {self.device}")
            
        except Exception as e:
            self.logger.error(f"Failed to load model: {str(e)}")
            raise
    
    def normalize_features(self, pod: PodRequest, node: NodeCandidate) -> np.ndarray:
        """Normalize features to match training data distribution"""
        
        # These normalization factors should match your training data
        # You may need to adjust these based on your actual data ranges
        MAX_CPU_MILLI = 50000  # 50 CPU cores
        MAX_RAM_MIB = 100000   # ~100GB RAM
        MAX_ENERGY = 1000.0    # Adjust based on your energy scale
        
        features = np.array([
            pod.cpu_request_milli / MAX_CPU_MILLI,
            pod.ram_request_mib / MAX_RAM_MIB,
            node.cpu_total_milli / MAX_CPU_MILLI,
            node.ram_total_mib / MAX_RAM_MIB,
            node.cpu_model_energy_val / MAX_ENERGY,
            self.node_encoder.encode(node.node_type) / 3.0,  # Normalize node type
            node.energy_before / MAX_ENERGY
        ], dtype=np.float32)
        
        return features
    
    def predict_node_scores(self, pod: PodRequest, candidates: List[NodeCandidate]) -> List[Dict[str, Any]]:
        """
        Predict Q-values for all candidate nodes
        Returns list of dictionaries with node info and scores
        """
        if not self.model:
            raise RuntimeError("Model not loaded")
        
        scores = []
        
        with torch.no_grad():
            for node in candidates:
                try:
                    # Normalize features
                    features = self.normalize_features(pod, node)
                    
                    # Convert to tensor
                    state_tensor = torch.FloatTensor(features).unsqueeze(0).to(self.device)
                    
                    # Get Q-value
                    q_value = self.model(state_tensor).item()
                    
                    scores.append({
                        'node_name': node.name,
                        'q_value': float(q_value),
                        'energy_before': node.energy_before,
                        'node_type': node.node_type
                    })
                    
                except Exception as e:
                    self.logger.warning(f"Error processing node {node.name}: {str(e)}")
                    # Assign very low score for problematic nodes
                    scores.append({
                        'node_name': node.name,
                        'q_value': -1000.0,
                        'energy_before': node.energy_before,
                        'node_type': node.node_type,
                        'error': str(e)
                    })
        
        # Sort by Q-value (higher is better)
        scores.sort(key=lambda x: x['q_value'], reverse=True)
        return scores
    
    def get_best_node(self, pod: PodRequest, candidates: List[NodeCandidate]) -> Optional[str]:
        """Get the best node name for scheduling"""
        scores = self.predict_node_scores(pod, candidates)
        
        if not scores:
            return None
        
        best_node = scores[0]
        self.logger.info(f"Best node: {best_node['node_name']} (Q-value: {best_node['q_value']:.4f})")
        
        return best_node['node_name']

# Flask application
app = Flask(__name__)
app.logger.setLevel(logging.INFO)
# Global inference server instance
inference_server: Optional[DQNInferenceServer] = None

@app.route('/health', methods=['GET'])
def health_check():
    """Health check endpoint"""
    return jsonify({
        'status': 'healthy',
        'model_loaded': inference_server is not None and inference_server.model is not None
    })

@app.route('/predict', methods=['POST'])
def predict_node():
    """
    Main prediction endpoint
    Expected JSON format:
    {
        "pod": {
            "cpu_request_milli": 2000,
            "ram_request_mib": 1024
        },
        "candidates": [
            {
                "name": "node-1",
                "cpu_total_milli": 8000,
                "ram_total_mib": 16384,
                "cpu_model_energy_val": 150.5,
                "node_type": "cloud",
                "energy_before": 200.0
            },
            ...
        ]
    }
    """
    try:
        if not inference_server or not inference_server.model:
            return jsonify({'error': 'Model not loaded'}), 500
        
        # Parse request
        data = request.get_json()
        if not data:
            return jsonify({'error': 'No JSON data provided'}), 400
        
        # Validate required fields
        if 'pod' not in data or 'candidates' not in data:
            return jsonify({'error': 'Missing pod or candidates in request'}), 400
        
        # Parse pod request
        pod_data = data['pod']
        pod = PodRequest(
            cpu_request_milli=pod_data['cpu_request_milli'],
            ram_request_mib=pod_data['ram_request_mib']
        )
        
        # Parse candidates
        candidates = []
        for candidate_data in data['candidates']:
            candidate = NodeCandidate(
                name=candidate_data['name'],
                cpu_total_milli=candidate_data['cpu_total_milli'],
                ram_total_mib=candidate_data['ram_total_mib'],
                cpu_model_energy_val=candidate_data['cpu_model_energy_val'],
                node_type=candidate_data['node_type'],
                energy_before=candidate_data['energy_before']
            )
            candidates.append(candidate)
        
        if not candidates:
            return jsonify({'error': 'No candidates provided'}), 400
        
        # Get prediction
        best_node = inference_server.get_best_node(pod, candidates)
        
        if not best_node:
            return jsonify({'error': 'No suitable node found'}), 404
        
        # Return result
        return jsonify({
            'recommended_node': best_node,
            'pod_cpu': pod.cpu_request_milli,
            'pod_ram': pod.ram_request_mib,
            'num_candidates': len(candidates)
        })
        
    except Exception as e:
        app.logger.error(f"Prediction error: {traceback.format_exc()}")
        return jsonify({'error': f'Prediction failed: {str(e)}'}), 500

@app.route('/predict/detailed', methods=['POST'])
def predict_detailed():
    """
    Detailed prediction endpoint returning scores for all nodes
    Same input format as /predict
    """
    try:
        if not inference_server or not inference_server.model:
            return jsonify({'error': 'Model not loaded'}), 500
        
        # Parse request (same as above)
        data = request.get_json()
        if not data:
            return jsonify({'error': 'No JSON data provided'}), 400
        
        if 'pod' not in data or 'candidates' not in data:
            return jsonify({'error': 'Missing pod or candidates in request'}), 400
        
        pod_data = data['pod']
        pod = PodRequest(
            cpu_request_milli=pod_data['cpu_request_milli'],
            ram_request_mib=pod_data['ram_request_mib']
        )
        
        candidates = []
        for candidate_data in data['candidates']:
            candidate = NodeCandidate(
                name=candidate_data['name'],
                cpu_total_milli=candidate_data['cpu_total_milli'],
                ram_total_mib=candidate_data['ram_total_mib'],
                cpu_model_energy_val=candidate_data['cpu_model_energy_val'],
                node_type=candidate_data['node_type'],
                energy_before=candidate_data['energy_before']
            )
            candidates.append(candidate)
        
        if not candidates:
            return jsonify({'error': 'No candidates provided'}), 400
        
        # Get detailed scores
        scores = inference_server.predict_node_scores(pod, candidates)
        
        return jsonify({
            'node_scores': scores,
            'recommended_node': scores[0]['node_name'] if scores else None,
            'pod_cpu': pod.cpu_request_milli,
            'pod_ram': pod.ram_request_mib
        })
        
    except Exception as e:
        app.logger.error(f"Detailed prediction error: {traceback.format_exc()}")
        return jsonify({'error': f'Prediction failed: {str(e)}'}), 500

def initialize_server(model_path: str):
    """Initialize the inference server"""
    global inference_server
    
    try:
        inference_server = DQNInferenceServer(model_path)
        print(f"DQN Inference Server initialized with model: {model_path}")
    except Exception as e:
        print(f"Failed to initialize server: {str(e)}")
        raise

if __name__ == '__main__':
    import argparse
    
    parser = argparse.ArgumentParser(description='DQN Scheduler Inference Server')
    parser.add_argument('--model-path', default='models/dqn_scheduler_new.pth', help='Path to trained DQN model (.pth file)')
    parser.add_argument('--host', default='0.0.0.0', help='Host to bind to')
    parser.add_argument('--port', type=int, default=8080, help='Port to bind to')
    parser.add_argument('--debug', action='store_true', help='Enable debug mode')
    
    args = parser.parse_args()
    
    # Initialize server
    initialize_server(args.model_path)
    
    # Start Flask app
    print(f"Starting DQN Inference Server on {args.host}:{args.port}")
    app.run(host=args.host, port=args.port, debug=args.debug)