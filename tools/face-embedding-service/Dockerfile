FROM python:3.11-slim

# OpenCV runtime libraries needed by InsightFace
RUN apt-get update && apt-get install -y --no-install-recommends \
        libglib2.0-0 libgl1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py .

ENV PORT=8090
EXPOSE 8090

# The model pack (~300 MB) is downloaded into /root/.insightface on first run.
# Mount a volume there to cache it across container restarts:
#   docker run -v arozos_face_models:/root/.insightface ...
CMD ["python", "app.py"]
