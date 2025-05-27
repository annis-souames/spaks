import grpc
from concurrent import futures
import time
import json
import numpy as np
import joblib
import logging

# Import generated gRPC code
import energy_pb2
import energy_pb2_grpc

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class EnergyPredictor:
    def __init__(self, model_path='pickle/lgbm_model.pkl', target_encoding_path='target_encoding_model.json'):
        """Initialize the energy predictor with the LightGBM model and target encoding mapping"""
        self.model = None
        self.target_encoder_mapping = None
        self.model_path = model_path
        self.target_encoding_path = target_encoding_path
        self.load_models()
    
    def load_models(self):
        """Load the trained LightGBM model and target encoder mapping"""
        try:
            # Load LightGBM model
            self.model = joblib.load(self.model_path)
            
            # Load target encoder mapping for CPU_Model
            with open(self.target_encoding_path, 'r') as f:
                self.target_encoder_mapping = json.load(f)
            
            logger.info("Models loaded successfully!")
        except Exception as e:
            logger.error(f"Error loading models: {str(e)}")
            raise
    
    def encode_cpu_model(self, cpu_model):
        """Encode CPU model using the saved target encoder mapping"""
        # If CPU model exists in mapping, use it; otherwise use global mean
        if cpu_model in self.target_encoder_mapping:
            return self.target_encoder_mapping[cpu_model]
        else:
            # Use global mean as fallback for unknown CPU models
            global_mean = sum(self.target_encoder_mapping.values()) / len(self.target_encoder_mapping)
            logger.warning(f"Unknown CPU model: {cpu_model}, using global mean: {global_mean}")
            return global_mean
    
    def predict(self, features):
        """
        Predict power consumption based on features
        
        Args:
            features: A dict or list of dicts containing the required features
            
        Returns:
            Predictions as floats
        """
        if isinstance(features, dict):
            # Single prediction
            feature_array = self._prepare_features(features)
            prediction = self.model.predict(feature_array.reshape(1, -1))
            return prediction[0]
        else:
            # Batch prediction
            feature_arrays = []
            for feature_dict in features:
                feature_arrays.append(self._prepare_features(feature_dict))
            
            X = np.array(feature_arrays)
            predictions = self.model.predict(X)
            return predictions
    
    def _prepare_features(self, feature_dict):
        """Prepare feature array in the correct order for prediction"""
        cpu_model_encoded = self.encode_cpu_model(feature_dict["cpu_model"])
        ram_capacity = float(feature_dict["ram_capacity_gb"])
        cpu_freq = int(feature_dict["cpu_freq_mhz"])
        num_cores = int(feature_dict["num_cores"])
        achieved_load = float(feature_dict["achieved_load_pct"])
        
        # Create feature array in the correct order: 
        # [CPU_Model_encoded, RAM_Capacity_GB, CPU_Freq_MHz, Num_Cores, Achieved_Load_Pct]
        return np.array([cpu_model_encoded, ram_capacity, cpu_freq, num_cores, achieved_load])

class EnergyServicer(energy_pb2_grpc.EnergyServiceServicer):
    def __init__(self, predictor):
        self.predictor = predictor
    
    def PredictPower(self, request, context):
        """Handle single prediction request"""
        try:
            features = {
                "cpu_model": request.cpu_model,
                "ram_capacity_gb": request.ram_capacity_gb,
                "cpu_freq_mhz": request.cpu_freq_mhz,
                "num_cores": request.num_cores,
                "achieved_load_pct": request.achieved_load_pct
            }
            
            prediction = self.predictor.predict(features)
            
            return energy_pb2.PredictionResponse(
                predicted_power_watts=float(prediction),
                status="success"
            )
        except Exception as e:
            logger.error(f"Error in PredictPower: {str(e)}")
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(f"Error during prediction: {str(e)}")
            return energy_pb2.PredictionResponse(
                predicted_power_watts=0.0,
                status="error",
                error=str(e)
            )
    
    def PredictPowerBatch(self, request, context):
        """Handle batch prediction requests"""
        try:
            features_list = []
            
            for req in request.requests:
                features = {
                    "cpu_model": req.cpu_model,
                    "ram_capacity_gb": req.ram_capacity_gb,
                    "cpu_freq_mhz": req.cpu_freq_mhz,
                    "num_cores": req.num_cores,
                    "achieved_load_pct": req.achieved_load_pct
                }
                features_list.append(features)
            
            predictions = self.predictor.predict(features_list)
            
            response = energy_pb2.BatchPredictionResponse(status="success")
            for pred in predictions:
                response.predictions.append(float(pred))
            
            return response
        except Exception as e:
            logger.error(f"Error in PredictPowerBatch: {str(e)}")
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(f"Error during batch prediction: {str(e)}")
            return energy_pb2.BatchPredictionResponse(
                status="error",
                error=str(e)
            )

def serve(port=50051):
    """Start the gRPC server"""
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    predictor = EnergyPredictor()
    energy_pb2_grpc.add_EnergyServiceServicer_to_server(
        EnergyServicer(predictor), server
    )
    server.add_insecure_port(f'[::]:{port}')
    server.start()
    logger.info(f"Server started on port {port}")
    
    try:
        while True:
            time.sleep(86400)  # Sleep for a day
    except KeyboardInterrupt:
        server.stop(0)
        logger.info("Server stopped")

if __name__ == '__main__':
    serve()