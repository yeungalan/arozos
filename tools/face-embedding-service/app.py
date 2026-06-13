"""
ArozOS Face Embedding Service (reference implementation)

A small HTTP service that turns a face image into an embedding vector for the
ArozOS Photo app's deep face recognition ("External service" engine).

It implements the contract ArozOS expects:

    GET  /info   -> 200 {"dimension": 512, "model": "buffalo_l"}
    POST /embed  (body: image/jpeg of a face crop)
                 -> 200 {"embedding": [floats...]}            on success
                 -> 200 {"error": "no face detected"}         when no face is found
                 -> 4xx/5xx {"error": "..."}                  on failure

Detection, 5-point alignment and the ArcFace embedding are all done by
InsightFace. The returned embedding is already L2-normalized.

Environment variables:
    PORT                Port to listen on (default 8090)
    EMBEDDING_TOKEN     If set, requests must send "Authorization: Bearer <token>"
    INSIGHTFACE_MODEL   InsightFace model pack name (default "buffalo_l", 512-d)
    EMBEDDING_DIM       Embedding dimension reported by /info (default 512)
    DET_SIZE            Detector input size (default 320)

NOTE ON MODEL LICENSE: the default InsightFace model packs are released for
*non-commercial / research* use. That is fine for personal, self-hosted use.
For commercial use, supply a permissively-licensed model instead.
"""

import io
import os

import numpy as np
from flask import Flask, request, jsonify
from PIL import Image
from insightface.app import FaceAnalysis

TOKEN = os.environ.get("EMBEDDING_TOKEN", "").strip()
MODEL_NAME = os.environ.get("INSIGHTFACE_MODEL", "buffalo_l")
EMBEDDING_DIM = int(os.environ.get("EMBEDDING_DIM", "512"))
DET_SIZE = int(os.environ.get("DET_SIZE", "320"))

app = Flask(__name__)
_model = None


def get_model():
    """Lazily build (and cache) the InsightFace analyzer."""
    global _model
    if _model is None:
        m = FaceAnalysis(name=MODEL_NAME, providers=["CPUExecutionProvider"])
        m.prepare(ctx_id=-1, det_size=(DET_SIZE, DET_SIZE))
        _model = m
    return _model


def authorized(req):
    if not TOKEN:
        return True
    return req.headers.get("Authorization", "") == "Bearer " + TOKEN


@app.get("/info")
def info():
    if not authorized(request):
        return jsonify(error="unauthorized"), 401
    return jsonify(dimension=EMBEDDING_DIM, model=MODEL_NAME)


@app.post("/embed")
def embed():
    if not authorized(request):
        return jsonify(error="unauthorized"), 401

    data = request.get_data()
    if not data:
        return jsonify(error="empty request body"), 400

    try:
        image = Image.open(io.BytesIO(data)).convert("RGB")
    except Exception as exc:  # noqa: BLE001 - report any decode failure to the caller
        return jsonify(error="cannot decode image: %s" % exc), 400

    # InsightFace expects BGR
    frame = np.asarray(image)[:, :, ::-1]
    faces = get_model().get(frame)
    if not faces:
        return jsonify(error="no face detected"), 200

    # The ArozOS crop contains one main face; pick the largest just in case.
    face = max(faces, key=lambda f: (f.bbox[2] - f.bbox[0]) * (f.bbox[3] - f.bbox[1]))
    embedding = face.normed_embedding.astype(float).tolist()
    return jsonify(embedding=embedding)


if __name__ == "__main__":
    # Preload the model so the first real request is not slow.
    get_model()
    app.run(host="0.0.0.0", port=int(os.environ.get("PORT", "8090")))
