import numpy
import torch.nn as nn


class DQNScheduler(nn.Module):
    """
    Deep Q-Network for Kubernetes Pod Scheduling
    State: [pod_cpu, pod_ram, node_cpu, node_ram, node_energy_model, node_type, energy_before]
    Action: Node selection (implicit - each forward pass evaluates one node)
    """
    def __init__(self, state_dim=7, hidden_dims=[256, 128, 64], dropout=0.2):
        super(DQNScheduler, self).__init__()
        
        # Build network layers
        layers = []
        prev_dim = state_dim
        
        for hidden_dim in hidden_dims:
            layers.extend([
                nn.Linear(prev_dim, hidden_dim),
                nn.ReLU(),
                nn.Dropout(dropout)
            ])
            prev_dim = hidden_dim
        
        # Output layer - Q-value for this state-action pair
        layers.append(nn.Linear(prev_dim, 1))
        
        self.network = nn.Sequential(*layers)
        
    def forward(self, state):
        """Return Q-value for the given state"""
        return self.network(state)