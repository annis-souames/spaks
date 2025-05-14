from flask import Flask, request, jsonify
import logging
import random
logging.basicConfig(level=logging.DEBUG)

app = Flask(__name__)

@app.route('/cluster-info', methods=['POST'])
def cluster_info():
    """
    Receive a JSON dict of node availabilities, e.g.:
      {
        "node1": {"leftCPU": 500, "leftMemory": 1024},
        "node2": {"leftCPU": 250, "leftMemory": 2048}
      }
    Returns:
      { "score": <random int 1–100> }
    """
    data = request.get_json()
    if data is None:
        return jsonify({"error": "invalid or missing JSON"}), 400

    # (You could inspect or log `data` here if you like)
    logging.info("Received cluster info: %s", data)
    score = random.randint(1, 100)
    return jsonify({"score": score})

if __name__ == '__main__':
    # listens on all interfaces, port 5000
    app.run(host='0.0.0.0', port=5000)
