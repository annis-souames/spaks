# inference script that is imported to WASM runtime
import joblib
import numpy as np
import json

def load_model():
    global model
    model = joblib.load('model.joblib')

def predict(features_json):
    features = json.loads(features_json)
    features_array = np.array(features).reshape(1, -1)
    prediction = model.predict(features_array)
    return json.dumps(prediction.tolist())

# Auto-load model when module is imported
load_model()