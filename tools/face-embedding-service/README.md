# ArozOS Face Embedding Service

A small, self-hosted HTTP service that powers the **deep ("External service")
face recognition engine** of the ArozOS Photo app. ArozOS detects faces
locally and sends each face crop here; this service returns a face *embedding*
(a numeric vector). ArozOS then groups photos by comparing embeddings.

This reference implementation uses [InsightFace](https://github.com/deepinsight/insightface)
(SCRFD detector + 5-point alignment + ArcFace embedder) and runs on CPU, so it
works on any machine — including a Windows PC with Docker Desktop.

> **Why a separate service?** True deep face recognition needs a native ML
> runtime that cannot be bundled into the single, portable ArozOS binary
> (especially on Windows). Running it as a small service keeps ArozOS
> self-contained while giving you full-quality recognition.

## Run it with Docker (recommended)

```bash
cd tools/face-embedding-service
docker build -t arozos-face-embedding .

# Cache the downloaded model across restarts with a named volume.
docker run -d --name arozos-face-embedding \
    -p 8090:8090 \
    -v arozos_face_models:/root/.insightface \
    arozos-face-embedding
```

The first request downloads the model pack (~300 MB) into the volume; later
starts are fast.

To require a token, add `-e EMBEDDING_TOKEN=yoursecret` to `docker run`.

## Point ArozOS at it

In ArozOS: **System Settings → AI Integration → Face Recognition**

1. **Engine**: *Deep — External service (all platforms, incl. Windows)*
2. **Service URL**:
   - ArozOS running natively on the same machine: `http://localhost:8090`
   - ArozOS itself running in Docker: `http://host.docker.internal:8090`
3. **Access Token**: only if you set `EMBEDDING_TOKEN`.
4. Click **Test** — you should see `✓ Ready — embedding dimension 512`.
5. Turn the feature **on** and save.

The Photo app re-scans automatically and regroups people using the deep model.

## HTTP contract (for custom implementations)

You can replace this service with your own as long as it implements:

| Method & path | Request | Success response |
|---|---|---|
| `GET /info` | — | `200 {"dimension": <int>, "model": "<name>"}` |
| `POST /embed` | body: `image/jpeg` of a face crop | `200 {"embedding": [<floats>]}` |

- When no face is found, return `200 {"error": "no face detected"}` — ArozOS
  skips that face.
- If `EMBEDDING_TOKEN` is configured, ArozOS sends
  `Authorization: Bearer <token>` on every request.
- The embedding may be any fixed length; ArozOS L2-normalizes it and compares
  with cosine distance. Keep the dimension stable — changing it triggers a
  re-scan.

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `PORT` | `8090` | Listen port |
| `EMBEDDING_TOKEN` | _(none)_ | Require `Authorization: Bearer <token>` when set |
| `INSIGHTFACE_MODEL` | `buffalo_l` | InsightFace model pack |
| `EMBEDDING_DIM` | `512` | Dimension reported by `/info` |
| `DET_SIZE` | `320` | Detector input size |

## Running without Docker

```bash
pip install -r requirements.txt
python app.py
```

## ⚠️ Model license

The default InsightFace model packs are released for **non-commercial /
research use**. That is fine for personal, self-hosted use. For commercial
deployments, swap in a permissively-licensed model and adjust `app.py`
accordingly. ArozOS itself ships **no** model weights.
